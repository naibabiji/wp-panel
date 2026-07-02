# WP Panel

WP Panel is a WordPress-focused server management panel for Debian 13 VPS environments. It helps you provision and operate WordPress sites with a single Go binary, embedded templates, and a workflow centered on security, isolation, backups, SSL, PHP-FPM, Nginx, MariaDB, and daily site operations.

If you want the Chinese project README, see [README.md](README.md).

[![License](https://img.shields.io/badge/license-GPL--3.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8.svg)](https://go.dev/)

---

## Official Sources

- Official website: <https://wp-panel.org>
- GitHub repository: <https://github.com/naibabiji/wp-panel>

Any domain other than `wp-panel.org` and this GitHub repository is not an official WP Panel website.

---

## Positioning

Generic Linux panels usually get bloated, complicated, and full of features that have nothing to do with WordPress.

WP Panel focuses on one job: **running WordPress sites efficiently on VPS servers**. It does not try to be a Docker platform, mail server, FTP server, or Java/Python/Node hosting stack.

## Feature Overview

| Module | What it does |
|------|------|
| **Site management** | One-click site provisioning with isolated users, directories, Nginx, PHP-FPM, and databases; pause, enable, delete, and reinstall WordPress |
| **SSL certificates** | Automatic Let's Encrypt issuance, automatic renewal before expiry, manual replacement, and self-signed certificates |
| **FastCGI cache** | Full-site Nginx caching with one-click purge support from the bundled WordPress plugin |
| **Security defense** | Fail2ban + nftables progressive bans, allowlists for Cloudflare/Google/Bing, global rate limiting, and WordPress security event analysis |
| **Database management** | MariaDB password changes, database backup and restore, upload restore, and automatic backups |
| **Scheduled tasks** | Visual cron management, WP Cron replacement, incremental file backups, and system task inspection |
| **File manager** | Upload, download, delete, rename, archive, extract, cut, copy, paste, multi-select, and chunked upload with resume support |
| **Dashboard** | Live CPU, memory, disk, and load monitoring with 24h/7d/15d historical charts |
| **AI diagnostics** | One-click site health analysis with follow-up questions and diagnostic history, focused on logs and service-state clues |
| **Alerting** | SMTP mail alerts with independent switches for CPU, memory, disk, service, SSL, site expiry, system update, and panel update rules |
| **Software management** | PHP, Nginx, MariaDB, and Redis configuration editing, process supervision, and log viewing |
| **Panel security** | Random entry path + BasicAuth + web login, bcrypt password hashing, and login-failure bans |
| **Updates** | In-panel update checks, SHA256 + Ed25519 verification, automatic rollback on failure, and optional reverse-proxy support for China users |
| **Suspicious access analysis** | Aggregated analysis of WordPress access logs with risk levels, suggested actions, and suspicious IP/path evidence |
| **Crawler throttling** | Separate rate limits for bot user agents to reduce high-frequency scraper and script traffic |
| **Remote backups** | Backup sync to remote targets via rsync/SSH or S3-compatible object storage, with optional local copy retention |

## One-Click Installation

```bash
apt-get update && apt-get install -y wget ca-certificates && wget -qO- https://raw.githubusercontent.com/naibabiji/wp-panel/main/install.sh | bash
```

If GitHub is not reachable from your server, use the China-friendly installer:

```bash
apt-get update && apt-get install -y wget ca-certificates && wget -qO- https://gh.wp-panel.org/https://raw.githubusercontent.com/naibabiji/wp-panel/main/install-cn.sh | bash
```

After installation, the script prints the panel URL and the two login layers: BasicAuth and web login.

> The browser may warn about a self-signed certificate on the first visit. Click "Advanced" and continue.

## Quick Start

1. Install the panel with the one-line installer.
2. Open the panel URL printed by the installer.
3. Sign in with BasicAuth first, then complete the web login.
4. Run `wp info` to confirm the panel version, port, and entry path.
5. Run `wp status` to check whether the panel is healthy.
6. Use `wp restart` if you need a quick restart.
7. Use `wp password` if you need to reset the administrator password.
8. Use `wp unban` if the administrator account or your IP was banned by mistake.

If you need a fresh WordPress site, use the site management pages in the panel UI to create one. The panel will handle the isolated user, web root, PHP-FPM pool, and MariaDB database for that site.

## Security

**Short version: if the login URL and credentials stay private, outsiders do not get in.**

The panel uses four layers of protection:

- Layer 0: **scan defense** - non-browser requests that touch port 8443 are identified immediately and banned at the nftables network layer for 30 days
- Layer 1: **random entry path** - an 8-character random hex path gives 16^8 possible combinations, which makes blind scanning impractical
- Layer 2: **BasicAuth** - the browser prompts for a username and password
- Layer 3: **web login** - the page form asks for the panel password

Only users who pass all four layers can enter the panel. Five failures at any layer trigger a ban.

### Access Protection

- scan defense automatically detects non-browser traffic such as curl, scripts, and scanners on port 8443
- the random entry path is not meant to be guessed by brute force
- BasicAuth and web login provide two separate authentication checks
- traffic is encrypted over HTTPS only; the panel exposes only port 8443
- API error messages avoid leaking internal paths and command output

### Anti-Bruteforce

- five consecutive failures at any authentication layer trigger a 24-hour nftables ban
- progressive ban durations: 10 minutes, 24 hours, 30 days, then permanent

### Site Isolation

- every site runs under its own system user and PHP-FPM pool
- every site uses its own MariaDB database
- one broken site should not take down the others

### WordPress-Specific Protection

- automatically detects and bans brute-force attacks against `wp-login.php` and malicious `xmlrpc.php` requests
- scans for sensitive files such as `.env`, `.git`, and archives, then bans the source automatically
- detects 404 bursts and treats 30 hits in 60 seconds as a directory scan
- Nginx rejects HTTPS connections for unknown domains to avoid certificate leaks
- logged-in WordPress users are exempt from throttling so normal admin work stays smooth
- **WordPress suspicious access analysis**: aggregates `wp-security.log` and `error.log` in 30-second windows, then outputs high/medium/low risk events, matched paths, suspicious IPs, and suggested actions; by default it only analyzes and does not auto-ban
- **runtime security monitoring**: detects newly created suspicious PHP files and high-frequency access inside the `wp-content` runtime tree, then records file-security events
- **crawler throttling**: separate `bot_limit_enabled` / `bot_limit_rpm` / `bot_limit_burst` policies keep normal user traffic unaffected while constraining high-frequency crawlers and scanners

### AI Operations Diagnostics

- AI diagnostics aggregate logs, PHP/database/service state, and runtime context for each site
- follow-up questions stay inside the same session
- diagnostics are read-only; they do not perform file changes, database changes, or repair commands
- useful for spotting error trends, suggesting reproduction steps, and keeping a traceable recommendation history

### Backup and Offsite Archiving

- site backup tasks support S3 object storage
- endpoint reachability checks and transfer tests are available
- failed syncs can keep a local copy, and cleanup can be recovered according to the configured policy

### Update Safety

- panel updates use both SHA256 and Ed25519 checks, so a tampered GitHub Release cannot be forged into a valid package
- failed updates roll back to the previous version automatically

### Code Transparency

- 100% open source under GPL-3.0
- no sensitive business data is collected; anonymous stats are limited to the version number and can be disabled in the panel
- update checks connect only to GitHub, not to other external services
- no web shell and no online code editor
- passwords are stored with bcrypt cost 12, never in plain text
- three rounds of AI security review have already fixed 44 potential issues

### Deep-Dive Security Notes

- **[Install script security transparency report](security/wp-panel-install-security.md)** - a section-by-section walkthrough of `install.sh`, including the common accusations about password tampering, Nginx removal, and WordPress compromise
- **[Runtime security: layered defense model](security/wp-panel-runtime-security.md)** - source-level explanation of the six-layer defense system, update signature checks, and software vulnerability management

## Security Testing

White-hat researchers are welcome to test this project. If you find a security issue, please report it in one of these ways:

- **Public report**: open a [GitHub Issue](https://github.com/naibabiji/wp-panel/issues) and prefix the title with `[Security]`
- **Private report**: submit a Private Vulnerability Report through the GitHub Security tab
- Valid reports will be acknowledged in the Release Notes after the issue is fixed

## System Requirements

| Item | Requirement |
|------|------|
| Operating system | Debian 13 (Trixie) |
| CPU | 1 core or more |
| Memory | 1 GB or more (Swap is created automatically below that) |
| Architecture | x86_64 |

> Cloud vendor custom images can introduce unknown problems. If installation is troublesome, reinstall to a clean Debian 13 system with [bin456789/reinstall](https://github.com/bin456789/reinstall) and try again.

## Why These Tech Choices

**Why Debian 13?**

Debian is one of the most stable server distributions available. Trixie (Debian 13) was the latest stable release when development started. It gives us a recent kernel, newer package versions, and Debian's usual conservative stability policy. That means long-term security updates without forcing users to upgrade their base OS too often.

**Why lock to PHP 8.3?**

The WordPress project recommends PHP 8.3 or newer. PHP 8.3 is already widely tested in real production environments across the WordPress ecosystem, and it still has an active support window. Keeping the version fixed makes the runtime consistent, which helps reproduce and debug issues without fighting PHP version drift.

**Why MariaDB instead of MySQL?**

WordPress recommends MariaDB 10.6 or newer, and Debian 12/13 ships compatible MariaDB releases out of the box. Oracle MySQL comes with license and feature constraints. MariaDB is a fully compatible GPL fork driven by the community, and Debian's LTS packages provide security updates through 2028 without third-party repositories.

**Why build a Go binary instead of using Docker or PM2?**

The panel ships as a single binary with zero extra runtime dependency and is managed by `systemd`. It uses only a few dozen megabytes of memory, which is a good fit for 1 GB VPS plans. It does not share ports with Nginx, and there is no container layer or runtime overhead.

## Runtime Components

All runtime components are installed through APT packages; the panel does not compile them itself:

| Component | Notes |
|------|------|
| PHP 8.3 | Installed from Ondřej Surý's repository with isolated FPM pools |
| MariaDB | Debian-provided LTS release |
| Nginx | Debian stable package |
| Redis | Debian package |
| Fail2ban + nftables | Debian package |

## Technical Architecture

- **Backend**: Go + Gin web framework, SQLite in WAL mode, listening on port 8443 over HTTPS/TLS
- **Frontend**: HTML templates + TailwindCSS + Alpine.js + Chart.js
- **Distribution**: single binary with embedded frontend assets via `//go:embed`, around 20 MB
- **Security**: the panel is not tied to an Nginx reverse proxy and provides its own TLS termination

## SSH Management Commands

After installation, the panel provides a `wp` command-line helper:

| Command | Description |
|------|------|
| `wp` | Show panel information |
| `wp restart` | Restart the panel |
| `wp password` | Reset the administrator password in one step |
| `wp info` | Show version, port, and entry path |
| `wp status` | Show runtime status |
| `wp unban` | Clear all IP bans for emergency recovery |

## Panel Database Backup and Restore

The panel stores its own data in SQLite and creates automatic backups every day at 2:30 AM, keeping the latest 7 copies in `/www/server/panel/backups/panel-db/`.

### When the Panel Is Working

From the "Panel Settings" page you can:

- create a backup manually
- download a backup file locally
- restore from a backup with an automatic safety backup before the restore
- delete backups

### Recovery When the Panel Cannot Start

If the panel cannot start after a database restore, or the database is damaged and the panel is no longer usable, recover it manually over SSH:

```bash
# 1. Check available backups
ls -lh /www/server/panel/backups/panel-db/

# 2. Stop the panel
systemctl stop wp-panel

# 3. Back up the damaged database first, just in case
cp /www/server/panel/panel.db /www/server/panel/panel.db.broken

# 4. Replace the current database with a backup file
cp /www/server/panel/backups/panel-db/panel_20260107_023000.db /www/server/panel/panel.db

# 5. Start the panel
systemctl start wp-panel

# 6. Check whether it is healthy
systemctl status wp-panel
journalctl -u wp-panel -n 20
```

### Importing a Backup After Reinstalling the Panel

If you need to reinstall the panel completely and then restore data:

```bash
# 1. Copy the backup set to a safe location
cp -r /www/server/panel/backups/panel-db/ /root/panel-db-backup/

# 2. Reinstall the panel (choose uninstall then reinstall, and keep site data)

# 3. Stop the panel after installation
systemctl stop wp-panel

# 4. Replace the new database with your backup
cp /root/panel-db-backup/panel_20260107_023000.db /www/server/panel/panel.db

# 5. Start the panel and let the database upgrade chain run
systemctl start wp-panel
```

> Older backups may miss newer database fields. When the panel starts, the upgrade chain will fill them in automatically.

## FAQ

### Does WP Panel need Docker?

No. WP Panel ships as a single Go binary and is managed by `systemd`.

### Does the panel use SQLite or MySQL?

The panel itself uses SQLite for panel state. WordPress sites use MariaDB databases.

### Does the panel listen behind a reverse proxy?

No. The panel serves HTTPS on its own and is not designed to depend on an Nginx reverse proxy.

### Can I restore a panel backup after reinstalling?

Yes. The README includes a manual restore flow for both a running panel and a reinstall recovery scenario.

### Can I sync site backups to remote storage?

Yes. Remote backup sync supports rsync/SSH and S3-compatible object storage.

### What if GitHub is blocked from my server?

Use the China-friendly `install-cn.sh` installer.

## Project Structure

```text
├── main.go               # program entry
├── config/               # global config management
├── database/             # SQLite connection and migrations
├── models/               # data structures
├── router/               # routes and page dispatch
├── middleware/           # BasicAuth / Session / CSRF / login throttling
├── handlers/             # HTTP handlers
├── executor/             # task executor
├── collector/            # system metrics collector
├── templates/            # HTML templates
├── static/               # JS assets
├── input.css             # TailwindCSS source
├── install.sh            # one-click installer
├── install-cn.sh         # China-friendly installer
├── security/             # security notes
└── wp-panel-optimizer/   # bundled WordPress plugin
```

## License

GPL-3.0
