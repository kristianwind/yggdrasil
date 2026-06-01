# Community Runes

Extra gameskills ("Runes") that are **not bundled** with Yggdrasil — they're
experimental, community-maintained, or depend on game files we can't ship.

To use one: open **Runes → Carve a rune (upload)** in the panel and upload the
`.yaml` file from this folder. It's then available when creating a server.

> These are provided as-is. Only run game servers you are legally permitted to.

## Available

### `genshin-impact.yaml` — Genshin Impact (Grasscutter)

Genshin Impact has no official dedicated server. This rune runs
[Grasscutter](https://github.com/Grasscutters/Grasscutter), an open-source
private-server emulator. It is a **starter template** — you must provide:

- **MongoDB** reachable from the container (set `MONGO_URI` when creating the
  server). Run it as another server or on the host.
- **`grasscutter.jar`** — upload it into the server's files (Files tab).
- **`Resources/`** game data — upload via the Files tab. Not downloaded
  automatically.

After uploading those, press **Start**. See the Grasscutter wiki for client
redirection. The install step writes a `config.json` from your chosen ports +
`MONGO_URI`; edit it via the Files tab for anything deeper.
