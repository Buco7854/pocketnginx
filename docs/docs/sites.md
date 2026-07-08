import ThemedImage from "@theme/ThemedImage";
import useBaseUrl from "@docusaurus/useBaseUrl";

# Sites and streams

Each row shows a vhost's domains as badges: `server_name` for HTTP sites, and
`listen` to `proxy_pass` for streams. The filter box narrows the list. Enable
and maintenance are per-row toggles applied right away. Tick several rows to
reveal the bulk action bar, which asks for confirmation before it acts. Click a
row to open its editor, which offers the same actions plus rename and delete.

<ThemedImage
  alt="The sites list"
  sources={{
    light: useBaseUrl("/img/screenshot-sites.png"),
    dark: useBaseUrl("/img/screenshot-sites-dark.png"),
  }}
/>

## Enable and disable

Enable and disable manage the classic Debian symlinks between
`sites-available` and `sites-enabled`. The action refuses to touch anything
that is not a symlink, so it never removes a real file by mistake.

## Maintenance mode

Maintenance mode reads the site file and extracts `listen`, `server_name`,
`ssl_certificate` and related directives, and the `http2` and `http3` flags. It
writes a generated 503 vhost under `<conf dir>/.lightngx/maintenance/` and
repoints the `sites-enabled` symlink at it. Every step is validated with
`nginx -t` and rolled back on failure, after which nginx reloads.

:::note Includes are not copied
Server-level `include` directives inside a site file are not copied into the
maintenance vhost. If your certificates come from an include, add them to the
site file directly so the maintenance page can serve them.
:::

## Streams

Streams work the same way under `streams-available` and `streams-enabled` for
the `stream{}` context (TCP and UDP), without maintenance mode. A stream row
badges its `listen` to `proxy_pass` target instead of domains.

<ThemedImage
  alt="The streams list"
  sources={{
    light: useBaseUrl("/img/screenshot-streams.png"),
    dark: useBaseUrl("/img/screenshot-streams-dark.png"),
  }}
/>
