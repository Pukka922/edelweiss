package service

// initScript is /etc/init.d/edelweiss; @COMMAND@ is replaced by the binary path.
const initScript = `#!/sbin/openrc-run
# Written by "edelweissd service install" and replaced by every new install.
# Settings belong into /etc/conf.d/edelweiss.

name="edelweiss"
description="edelweiss web administration"

supervisor=supervise-daemon
command="@COMMAND@"
command_args="serve --config ${EDELWEISS_CONFIG:-/etc/edelweiss/edelweiss.toml} ${EDELWEISS_ARGS}"
respawn_delay=2
respawn_max=5
# edelweissd finishes open requests for up to 10 s and then stops its extensions (up to 8 s)
retry="TERM/30/KILL/5"
output_logger="logger -t edelweiss"
error_logger="logger -t edelweiss"

depend() {
	use net logger
	after firewall
}
`

const confD = `# Settings for /etc/init.d/edelweiss.
# Written once by "edelweissd service install" and kept on reinstall.

# Configuration file of edelweissd.
#EDELWEISS_CONFIG="/etc/edelweiss/edelweiss.toml"

# Additional arguments for "edelweissd serve".
#EDELWEISS_ARGS=""

# File descriptors: every extension process and every connection needs some.
rc_ulimit="-n 65536"
`

// DefaultConfig is the configuration file "service install" writes when none exists: the built-in defaults, spelled
// out so they can be changed in place.
const DefaultConfig = `# edelweiss configuration, written by "edelweissd service install".
# The values are the built-in defaults. After a change: rc-service edelweiss restart

listen = "0.0.0.0:9090"

[tls]
enabled = true
# Empty cert and key: a self-signed certificate is created in /var/lib/edelweiss/tls and renewed automatically.
cert = ""
key = ""

[auth]
admin_group = "wheel"
deny_users = ["root"]
session_idle_timeout = "15m"
session_max_age = "12h"
shadow_file = "/etc/shadow"

[extensions]
backend_idle_timeout = "10m"
max_download_size = "200MB"

[github]
api_url = "https://api.github.com"
# Optional: a token raises the API rate limit and gives access to private repositories.
token = ""

[log]
level = "info"

[paths]
state_dir = "/var/lib/edelweiss"
run_dir = "/run/edelweiss"
`
