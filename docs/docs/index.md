---
slug: /
sidebar_label: Overview
---

import ThemedImage from "@theme/ThemedImage";
import useBaseUrl from "@docusaurus/useBaseUrl";

# Lightngx

A lightweight web UI for managing nginx.

Lightngx lets you edit nginx configuration with syntax highlighting, enable
and disable sites, put them in maintenance, tail logs live, and reload or
restart. It is a single static Go binary with the React frontend embedded, so
there is no database server and no runtime dependency to install. Run it as one
container, or drop the binary next to your existing nginx.

<ThemedImage
  alt="The Lightngx configuration editor"
  sources={{
    light: useBaseUrl("/img/screenshot-editor.png"),
    dark: useBaseUrl("/img/screenshot-editor-dark.png"),
  }}
/>

## What it does

- **Guarded config editor.** A file browser with nginx syntax highlighting
  (CodeMirror 6). Every write, rename, delete and toggle runs `nginx -t` first
  and rolls back if the test fails, showing you the error. You cannot break the
  running config from the UI.

- **Sites and streams.** Manage the Debian `sites-available` and
  `sites-enabled` layout, plus `streams-*` for the `stream{}` context, from a
  selectable list with bulk enable, disable, maintenance and delete.

- **Maintenance mode.** One click swaps a site to a generated 503 page that
  reuses its `listen`, `server_name` and TLS certificates. One click puts it
  back.

- **Live logs.** Follow over server-sent events, page backwards through history
  (rotated `.gz` files included), and filter or color warnings and errors in
  the browser.

- **nginx control.** Test, reload with SIGHUP, and restart. Inside the
  container the binary supervises nginx as a child process, so the UI stays
  reachable to fix a broken config even when nginx will not start.

- **Accounts, roles and MFA.** Local users (admin or user) in an embedded
  SQLite database, two-factor auth via TOTP and WebAuthn, an admin-controlled
  policy for which roles must use it, and optional OIDC login.

- **Themes and languages.** Dark, light and system themes. English and French.

Everything works on mobile: native text selection, copy and paste, and no zoom
traps. A left sidebar carries the pages (Config, Sites, Streams, Logs, Profile,
Admin). The navbar holds nginx status and operations, the theme switch and the
language picker.

<ThemedImage
  alt="The config file browser"
  sources={{
    light: useBaseUrl("/img/screenshot-config.png"),
    dark: useBaseUrl("/img/screenshot-config-dark.png"),
  }}
/>

## Philosophy

Lightngx is a project I build and maintain first of all for my own
servers. The whole point is that it stays light: a small, focused UI that
does the everyday nginx chores well, instead of growing into yet another
bloated control panel.

That shapes how it is maintained. Reported bugs get fixed and dependencies
stay current, but new features land rarely and deliberately. Feature
requests are welcome, and one that fits this philosophy and catches my
interest may well get built. Just do not expect a steady stream of new
functionality.

## Where to go next

- [System requirements](./requirements.md) is the short list of what it
  needs.
- [Getting started](./getting-started.md) walks through a first run with Docker
  Compose, and [Running without Docker](./without-docker.md) covers the bare
  binary.
- [Configuration](./configuration.md) is the full list of environment
  variables.
- [Light and full images](./images.md) explains the two image flavours and the
  CrowdSec, VTS and auth-gate extras.
