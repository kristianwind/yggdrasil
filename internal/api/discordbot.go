package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/google/uuid"
)

// Two-way Discord control bot. A gateway (outbound WebSocket) connection — no
// public endpoint or port-forward needed — exposes slash commands: read-only
// /status, /players, /servers to anyone the bot can see, and /start, /stop,
// /restart gated to a single configured control channel (so only people with
// access to that private channel can control servers). The token lives in
// Settings, encrypted at rest; the panel never logs it.

type discordBot struct {
	session *discordgo.Session
	channel string // control-channel ID; control commands are refused elsewhere
}

// startDiscordBot (re)connects the bot from the stored settings. Safe to call at
// startup and again after a settings change — it tears down any existing session
// first. A no-op (and a clean disconnect) when no token is set.
func (s *Server) startDiscordBot() {
	defer recoverLog("startDiscordBot")

	token := ""
	if enc := s.getSetting(context.Background(), "discord_bot_token"); enc != "" {
		if plain, err := s.cipher.Decrypt(enc); err == nil {
			token = plain
		}
	}
	channel := s.getSetting(context.Background(), "discord_bot_control_channel")

	s.botMu.Lock()
	if s.bot != nil {
		if s.bot.session != nil {
			s.bot.session.Close()
		}
		s.bot = nil
	}
	s.botMu.Unlock()

	if token == "" {
		return
	}

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Printf("discord bot: init: %v", err)
		return
	}
	dg.Identify.Intents = discordgo.IntentsGuilds // slash commands need no message-content intent
	dg.AddHandler(func(sess *discordgo.Session, ic *discordgo.InteractionCreate) {
		s.handleBotInteraction(sess, ic, channel)
	})
	// Register commands per guild the bot is in — guild commands appear instantly,
	// where global ones can take up to an hour the first time. GuildCreate fires for
	// each of the bot's guilds shortly after connect (and on any new join).
	dg.AddHandler(func(sess *discordgo.Session, gc *discordgo.GuildCreate) {
		s.registerBotCommands(sess, gc.ID)
	})
	if err := dg.Open(); err != nil {
		log.Printf("discord bot: connect: %v", err)
		return
	}

	s.botMu.Lock()
	s.bot = &discordBot{session: dg, channel: channel}
	s.botMu.Unlock()
	log.Printf("discord bot: connected%s", func() string {
		if channel == "" {
			return " (read-only — no control channel set)"
		}
		return ""
	}())
}

func serverNameOption() []*discordgo.ApplicationCommandOption {
	return []*discordgo.ApplicationCommandOption{{
		Type:        discordgo.ApplicationCommandOptionString,
		Name:        "server",
		Description: "The server name",
		Required:    true,
	}}
}

func (s *Server) registerBotCommands(dg *discordgo.Session, guildID string) {
	cmds := []*discordgo.ApplicationCommand{
		{Name: "status", Description: "Show every server and its status"},
		{Name: "servers", Description: "List servers"},
		{Name: "players", Description: "Show players online per server"},
		{Name: "start", Description: "Start a server", Options: serverNameOption()},
		{Name: "stop", Description: "Stop a server", Options: serverNameOption()},
		{Name: "restart", Description: "Restart a server", Options: serverNameOption()},
	}
	for _, c := range cmds {
		if _, err := dg.ApplicationCommandCreate(dg.State.User.ID, guildID, c); err != nil {
			log.Printf("discord bot: register %q in guild %s: %v", c.Name, guildID, err)
		}
	}
}

func (s *Server) handleBotInteraction(sess *discordgo.Session, ic *discordgo.InteractionCreate, controlChannel string) {
	defer recoverLog("handleBotInteraction")
	if ic.Type != discordgo.InteractionApplicationCommand {
		return
	}
	data := ic.ApplicationCommandData()
	reply := func(msg string) {
		sess.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: msg},
		})
	}

	switch data.Name {
	case "status", "servers":
		reply(s.botStatusText())
	case "players":
		reply(s.botPlayersText())
	case "start", "stop", "restart":
		// Gate: control commands only in the configured control channel.
		if controlChannel == "" || ic.ChannelID != controlChannel {
			reply("⛔ Control commands are only allowed in the panel's configured control channel.")
			return
		}
		name := ""
		if len(data.Options) > 0 {
			name = strings.TrimSpace(data.Options[0].StringValue())
		}
		reply(fmt.Sprintf("⏳ %s: **%s**…", data.Name, name))
		go s.botControl(sess, ic, data.Name, name)
	}
}

// botControl runs a control action and follows up with the result. Runs in its own
// goroutine because start/restart block on a container (re)create.
func (s *Server) botControl(sess *discordgo.Session, ic *discordgo.InteractionCreate, action, name string) {
	defer recoverLog("botControl")
	followup := func(msg string) {
		sess.FollowupMessageCreate(ic.Interaction, false, &discordgo.WebhookParams{Content: msg})
	}
	id, srvName, err := s.serverIDByName(name)
	if err != nil {
		followup(fmt.Sprintf("❓ No server named **%s**. Try `/servers` to see the names.", name))
		return
	}
	ctx := context.Background()
	switch action {
	case "start", "restart":
		if err := s.recreateAndStart(ctx, id); err != nil {
			followup(fmt.Sprintf("❌ %s **%s** failed: %v", action, srvName, err))
			return
		}
		s.clearWatchdog(id)
		s.clearStartWatch(id)
	case "stop":
		if err := s.botStop(ctx, id); err != nil {
			followup(fmt.Sprintf("❌ stop **%s** failed: %v", srvName, err))
			return
		}
	}
	actor := "discord"
	if ic.Member != nil && ic.Member.User != nil {
		actor = "discord:" + ic.Member.User.Username
	}
	s.auditSystem("server."+action, "server:"+id, actor, map[string]any{"via": "discord", "server": srvName})
	s.notifyServer(id, fmt.Sprintf("🎮 %s %sed **%s** via Discord", actor, action, srvName))
	followup(fmt.Sprintf("✅ %s: **%s**", action, srvName))
}

// botStop mirrors the HTTP stop handler's core: a graceful shutdown, then mark
// stopped and release forwarding.
func (s *Server) botStop(ctx context.Context, id string) error {
	srv, err := s.getServer(ctx, id)
	if err != nil {
		return err
	}
	if srv.ContainerID != "" {
		if rt, err := s.loadRuntime(ctx, id); err == nil {
			if err := s.gracefulStop(ctx, srv.ContainerID, rt.gs); err != nil {
				return err
			}
		} else if err := s.docker.Stop(ctx, srv.ContainerID, defaultStopTimeout); err != nil {
			return err
		}
	}
	s.db.ExecContext(ctx, "UPDATE servers SET status='stopped' WHERE id=?", id)
	s.clearStartWatch(id)
	s.clearResourceAlarms(id)
	s.stoppedCleanup(id)
	return nil
}

// serverIDByName resolves a server by its (case-insensitive) name.
func (s *Server) serverIDByName(name string) (id, canonical string, err error) {
	err = s.db.QueryRow("SELECT id, name FROM servers WHERE name=? COLLATE NOCASE", name).Scan(&id, &canonical)
	return
}

func (s *Server) botStatusText() string {
	rows, err := s.db.Query("SELECT name, status FROM servers ORDER BY name COLLATE NOCASE")
	if err != nil {
		return "⚠️ Couldn't read the server list."
	}
	defer rows.Close()
	var b strings.Builder
	b.WriteString("**Servers**\n")
	n := 0
	for rows.Next() {
		var name, status string
		if rows.Scan(&name, &status) != nil {
			continue
		}
		icon := "⚪"
		switch status {
		case "running":
			icon = "🟢"
		case "starting":
			icon = "🟡"
		}
		b.WriteString(fmt.Sprintf("%s %s — %s\n", icon, name, status))
		n++
	}
	if n == 0 {
		return "No servers yet."
	}
	return b.String()
}

func (s *Server) botPlayersText() string {
	rows, err := s.db.Query("SELECT id, name FROM servers WHERE status='running' ORDER BY name COLLATE NOCASE")
	if err != nil {
		return "⚠️ Couldn't read the server list."
	}
	type sv struct{ id, name string }
	var list []sv
	for rows.Next() {
		var x sv
		if rows.Scan(&x.id, &x.name) == nil {
			list = append(list, x)
		}
	}
	rows.Close()
	if len(list) == 0 {
		return "No servers are running."
	}
	sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
	var b strings.Builder
	b.WriteString("**Players online**\n")
	for _, x := range list {
		p := s.playersOnline(x.id)
		if p < 0 {
			b.WriteString(fmt.Sprintf("• %s — (no player query)\n", x.name))
		} else {
			b.WriteString(fmt.Sprintf("• %s — %d\n", x.name, p))
		}
	}
	return b.String()
}

// auditSystem records an action taken outside an HTTP request (e.g. the Discord
// bot), so control actions are still attributable in the audit log.
func (s *Server) auditSystem(action, resource, username string, detail map[string]any) {
	detailJSON := "null"
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			detailJSON = string(b)
		}
	}
	s.db.Exec(
		"INSERT INTO audit_log (id, user_id, username, action, resource, detail_json, ip, ts) VALUES (?,?,?,?,?,?,?,?)",
		uuid.New().String(), "", username, action, resource, detailJSON, "discord",
		time.Now().UTC().Format(time.RFC3339),
	)
}
