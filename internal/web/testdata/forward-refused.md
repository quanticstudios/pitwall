A TCP port here to the daemon's socket there, which any local user could
connect to:

    ssh -fN -L 127.0.0.1:7000:/run/user/1000/pitwall/pitwall.sock box

The phone's tunnel stays fine:

    ssh -L 7777:127.0.0.1:7777 your-computer
