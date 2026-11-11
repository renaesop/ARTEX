---
name: rea
description: "Inspect authorized task artifacts on the M6 Mac using offline static analysis of JavaScript/Electron, APK signatures, .NET artifacts, SQLite schema, ELF notes and Apple plists."
mcps: rea
---

Load this skill to inspect a specific local artifact. Use SearchExtraTools to select a tool under `mcp__rea__`, then ExecuteExtraTool. Every tool takes one `path`: the absolute path of an artifact inside **the current ARTEX task** on the US server. ARTEX transfers that sample over Tailscale to the M6, invokes REA there and returns its evidence. Select the smallest relevant sample; the input is removed from the Mac after the call.

- For a JS app select the app directory or an ASAR. For an extension, select its unpacked code directory.
- For an APK use `inspect_apk_signature` to inspect signing data. For an ELF use `inspect_elf_notes`; for Apple metadata pass the .app directory to `inspect_plist`. Tool errors report missing prerequisites.
- Inputs must consist of regular files, contain no symlinks, and fit within 768 MiB and 4096 files. Avoid selecting a worker workspace, live browser profile or unrelated data. If the Mac is busy, retry later.
- Calls are independent. Keep relevant source locations, observations and limitations in the ARTEX task for later use. A transient REA evidence ID cannot reopen a finished session.
