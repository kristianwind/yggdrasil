# Yggdrasil Panel — landing page

A self-contained static landing page for Yggdrasil Panel, built from the project's
README copy + screenshots. Single `index.html` (inline CSS, no build step) plus a
`screenshots/` folder.

## Host it with Yggdrasil itself

1. **Runes → Browse GitHub →** install **“Static site (nginx)”**.
2. **Create server** from that rune, then **Start** it.
3. Open the server's **Files** tab and **drag &amp; drop** this whole `website/` folder
   (or its contents) into the web root.
4. Give the server a **Subdomain** (Settings → Network: NPM or Cloudflare Tunnel),
   or point a Cloudflare Tunnel public hostname at the server's web port.

That's the panel hosting its own marketing site. 🌳
