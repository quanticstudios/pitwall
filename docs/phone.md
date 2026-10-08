# Phone

pitwall can serve a small page where your phone sees which agents need you
and answers them: Allow or Deny on a permission prompt, a typed reply to a
question or a finished turn. It runs inside pitwall's background service.
There is no account, no relay and no app to install: the phone talks to your
computer over your own network.

It is off until you turn it on in Settings, Phone, or in config.toml:

```toml
[remote]
enabled = true
```

By default it listens on `127.0.0.1:7777`, which only your computer can
reach. The way to get it onto your phone is your tailnet. With
[Tailscale](https://tailscale.com) on both devices:

```sh
tailscale serve --bg 7777
```

That prints an address such as `https://laptop.tail1234.ts.net`, with a
certificate Tailscale manages and reachable only from your own devices. Put
it in the config so the pairing code points there:

```toml
[remote]
enabled = true
url = "https://laptop.tail1234.ts.net"
```

An SSH tunnel works too: `ssh -L 7777:127.0.0.1:7777 your-computer` from a
phone SSH client, then open `http://127.0.0.1:7777`.

## Pair a phone

Run `pitwall remote pair`, or press Pair in Settings, Phone. It shows a QR
code and a URL holding a one-time code, good for 10 minutes. Scan it and the
page trades the code for a device token, which the phone keeps; pitwall keeps
only the token's SHA-256 under `remote/` in the state folder (mode 0600). A
request without a valid token gets nothing. After 10 failed codes or tokens
in a minute, every request is refused until the minute is up.

| Command                         | Does                                              |
| ------------------------------- | ------------------------------------------------- |
| `pitwall remote pair [name]`    | Pair a phone; prints the QR code and URL          |
| `pitwall remote devices`        | List paired devices                               |
| `pitwall remote revoke <name>`  | Unpair one by name or id; its token stops at once |

## What the page shows and sends

The page lists the tabs that need you: approvals, questions, finished turns
and errors, each with the agent's question or the tool it wants, and the
risk pitwall flags in a permission request. It shows what the sidebar shows,
never a pane's screen or scrollback. It asks for the list every 3 seconds
while it is open.

Allow presses the first option of the agent's prompt, Yes: `1` for Claude
Code and `y` for Codex. Deny presses Esc. pitwall sends either only while
the pane still shows the prompt the phone showed, and only when its screen
has a permission prompt on it. A reply loses its control characters, so it
cannot press keys or end a paste, and goes in as a paste followed by Enter.
pi has no permission prompts, so its tabs only take replies.

`daemon.log` gets a line for each pairing and each answer, with the device's
name: "allow sent to pane …", "reply sent to pane …", never what the reply
said.

## Another address

To serve on your network without Tailscale, set an address other than
loopback. pitwall then requires HTTPS, with a certificate it makes for
itself:

```toml
[remote]
enabled = true
listen = "0.0.0.0:7777"
tls = true
```

The phone's browser will warn that it does not know the certificate.
`pitwall remote pair` prints the certificate's SHA-256; compare it with the
one the browser shows before you continue. A non-loopback address without
`tls = true` is a config problem, and the page stays off.
