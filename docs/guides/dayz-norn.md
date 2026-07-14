# DayZ loot economy (Norn)

Norn is Yggdrasil's DayZ loot-economy helper: it reads your mission's economy files, raises item
lifetimes so modded loot stops vanishing, tunes the cleanup timers, and pulls mod loot into the
economy. It appears as a **Norn (loot)** tab on DayZ servers, next to a DayZ **Mods** tab.

Both tabs show up only when the server runs the `dayz` rune and you hold `server.control` on it.
The read endpoints behind them need `server.view`; anything that writes needs `server.control`.

## What Norn edits

Everything Norn touches is a file in the server's data directory, under the mission named by the
server's `MISSION` variable (default `dayzOffline.chernarusplus`):

- `mpmissions/<MISSION>/db/types.xml` — each item's `<lifetime>`, in seconds. This is what decides
  how long a dropped item stays in the world.
- `mpmissions/<MISSION>/db/globals.xml` — the `CleanupLifetime*` fallback timers.
- `mpmissions/<MISSION>/cfgeconomycore.xml` — the `<ce folder="…"><file name="…" type="types" /></ce>`
  registrations that tell DayZ which extra types files belong to the economy.

Norn also writes the modded types files it imports into `mpmissions/<MISSION>/<ModName>/`.

**Changes apply on the next server restart.** DayZ reads the economy at mission start.

## Reading the economy summary

Open the Norn tab and it summarizes the whole economy: the mission name, every types file currently
in play, the total number of item types across them, and the shortest lifetime it found. Each file is
listed with its item count, its own shortest lifetime, and a **modded** badge for anything that
isn't `db/types.xml`.

"Every types file currently in play" means exactly the set DayZ itself uses: `db/types.xml` plus each
file registered through a `<ce>` block in `cfgeconomycore.xml` that exists on disk. A types file that
isn't registered contributes nothing — and Norn calls that out separately (see below).

The shortest-lifetime figures ignore `0`. In DayZ a `<lifetime>` of `0` means the entry is unmanaged
(special or non-spawning), so counting it would make every mission report a minimum of zero. The
summary shows the shortest *real* loot lifetime instead, and warns when it is under an hour.

## The minimum lifetime floor

This is the fix for "modded items despawn too fast". Enter a number of hours under **Minimum
lifetime floor** and apply it: Norn walks every registered types file and raises every `<lifetime>`
that is above zero and below the floor up to the floor. Anything already above it is left alone, and
`0` entries are skipped so unmanaged items stay unmanaged.

It is a targeted numeric edit — only the numbers inside `<lifetime>` tags change. Comments,
indentation, ordering and everything else in the file survive untouched, so a hand-tuned types.xml
stays readable and diffable.

The response tells you how many lifetimes changed. Because the floor runs over the *registered* set,
register or import a mod's loot first if you want the floor to cover it.

## Cleanup timers (globals.xml)

**Cleanup timers** edits the `CleanupLifetime*` variables in `db/globals.xml`, in seconds. The one
that matters most is `CleanupLifetimeDefault`: it applies to items with no explicit lifetime, so
raise it if loot still disappears after you've set a floor.

Norn writes seven allow-listed variables and ignores anything else you send:
`CleanupLifetimeDefault`, `CleanupLifetimeRuined`, `CleanupLifetimeDeployed`,
`CleanupLifetimeDeadPlayer`, `CleanupLifetimeLimit`, `CleanupLifetimeDeadAnimal`,
`CleanupLifetimeDeadInfected`. Negative values are rejected. As with the floor, only the `value="…"`
number changes.

## Registering unregistered types.xml

Mission packs and mod install guides routinely drop a `types.xml` somewhere under the mission and
leave the `cfgeconomycore.xml` edit to you. Until it is registered, its items never spawn and no
floor covers them.

Norn scans the mission for exactly this case and lists what it finds under **modded loot file(s) not
in the economy**. **Register all in economy** adds a `<ce>` block for each, inserted before the
closing `</economycore>` tag, skipping any that are already registered.

The scan is deliberately narrow, to avoid registering a file that would break the economy:

- It looks at `.xml` files under the mission, skipping any directory whose name starts with
  `storage` (that's persistence, not config).
- A file qualifies only if it contains both `<type name=` and `<lifetime>`, which is what separates
  a loot table from `events.xml` or `cfgspawnabletypes.xml`, and it must have a `<types>` root so it
  can be registered as-is.
- `db/types.xml` and anything already registered are ignored.
- A file must sit in a subfolder — a `<ce folder="…">` entry needs one, so a stray types file in the
  mission root is listed but not registrable this way.

Both `cfgeconomycore.xml` and `cfgEconomyCore.xml` spellings are read.

## Mod loot

**Loot from installed mods** looks inside the `@<id>` folders in the data directory — the mods
SteamCMD downloaded — and lists the loot files each one ships, with an item count per file.

- **Mod names** come from `meta.cpp` inside the mod folder, falling back to the folder name, so the
  list reads "CodeLock" instead of `@1591180492`.
- **Fragments count.** Many mods ship a paste-into-your-types.xml snippet with no `<types>` root
  rather than a complete file. Norn detects those too: anything containing `<type name=` and
  `<lifetime>` is treated as importable loot, root or no root.
- **DayZ-Expansion mods are flagged.** Anything whose name or folder mentions "expansion" gets a
  *manages own economy* badge, because Expansion injects its loot itself — importing its types
  usually isn't needed.
- **Already-imported files** are badged rather than offered again.

**Import + register** does three things in one step: copies the file into
`mpmissions/<MISSION>/<ModName>/`, wrapping it in a `<types>` root first if it was a fragment, and
then registers it in `cfgeconomycore.xml`. The destination folder name is the mod's display name with
everything but letters, digits, `-` and `_` stripped. After the import the floor covers it like any
other registered file.

Imports are contained to the data directory and must come from an `@…` mod folder — a path pointing
anywhere else is rejected.

### An empty scan is usually correct

Most DayZ mods pack their configs into a `.pbo`. There is no loose `types.xml` on disk to find, and
Norn finding nothing for such a mod is the honest answer, not a failure. Those mods either register
their loot themselves at runtime or expect you to paste values from the Workshop page into the
mission by hand — in which case add the file under the mission yourself and use **Register all in
economy**.

## Persistence: surviving a reinstall

Norn saves every change you make against the server: the floor in hours, the globals values, and each
registration (with the mod file it came from, when it came from a mod). The Norn tab shows a
**Saved & auto-re-applied after updates** banner summarizing what is stored.

This matters because of how DayZ updates work. Update/Reinstall runs SteamCMD with `validate`, which
regenerates the vanilla mission files — and would silently throw away every lifetime you raised.
So at the end of every successful install, Yggdrasil re-applies the saved Norn config in order:
re-copy any missing registered file from its mod source (wrapping fragments again), re-register
everything in `cfgeconomycore.xml`, write the globals, then apply the lifetime floor last so it
covers the freshly registered files. The install log records that it happened.

Restarting a server does not touch these files. Only an install/reinstall regenerates them, and only
that path triggers the re-apply.

## Going back to vanilla

**Reset** clears the saved Norn config. It does not revert the files that are on disk right now —
it removes the instruction to re-apply. Run **Update/Reinstall** afterwards and SteamCMD regenerates
the vanilla mission economy with nothing re-applied on top.

## The Mods tab

The Mods tab exists because of a specific DayZ failure mode: when a Workshop item can't be
downloaded — removed upstream, made private, banned, or a typo'd id — the install logs a warning and
the server starts anyway with a *partial* mod list. Players then can't join, and the cause is buried
in an install log.

The tab reads the server's `MODS` variable (semicolon-, comma- or whitespace-separated Workshop ids,
in load order), and for each id shows two independent facts:

- **On disk / not downloaded** — whether an `@<id>` folder exists in the data directory.
- **Workshop ✓ / removed from Workshop / Workshop ?** — from one batch call to Steam's public
  Workshop API. No API key is needed. The call has a 6-second timeout, and if Steam is unreachable
  or the response won't parse, every id comes back as `?` (unknown) rather than being reported as
  removed.

A count of problem mods is shown at the top, and **Remove broken** strips them from the list.
Mod names come from the on-disk `meta.cpp` where available, otherwise the Workshop title, otherwise
the bare id.

**Orphans** are the other direction: `@<id>` folders on disk that aren't in `MODS`, so the server
doesn't load them. **Add to list** appends one to the load order.

Editing the mod list only writes the `MODS` variable — press **Update/Reinstall** to actually
download and apply it.

## See also

- [Servers](servers.md) — variables, install/update, and restarts
- [Monitoring and alerts](monitoring-and-alerts.md)
- [Kvasir](kvasir-ai.md) — the config advisor and the log explainer
- [API reference](../reference/api.md)
