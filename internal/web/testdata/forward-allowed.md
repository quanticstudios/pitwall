The phone's tunnel, a TCP port to a TCP port:

    ssh -L 7777:127.0.0.1:7777 your-computer

The daemon's socket, forwarded to a socket in a private folder:

    ssh -fN -L /run/user/1000/pitwall/box.sock:/run/user/1000/pitwall/pitwall.sock box
