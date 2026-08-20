# GitHub Antibot

Tired of spammy notifications from mass-following bot accounts? This GitHub Action automatically blocks suspicious users who follow you, keeping your follower list clean and your notifications relevant.

## How It Works

1. The action runs on a schedule (or on-demand via workflow dispatch)
2. It reads your settings from `config.json`
3. It fetches your current followers
4. For each follower it applies, in order:
   - **Whitelist** (`whitelist.txt`): never blocked, skipped entirely
   - **Blacklist** (`blacklist.txt`): blocked immediately whatever their following count
   - **Already blocked** (persisted in `data/blocked.json`): skipped, no repeat API calls
   - **Threshold**: users following more than the configured threshold are blocked
5. Results are persisted, reported, and committed back to the repository

## Usage

1. **Fork this repository** to your GitHub account
2. **Create a Personal Access Token** (see [PAT Configuration](#pat-configuration))
3. **Add the token as a repository secret** named `GH_PAT`
4. **Configure** [config.json](./config.json), [whitelist.txt](./whitelist.txt) and [blacklist.txt](./blacklist.txt)
5. **Enable GitHub Actions** on your fork if not already enabled

## Configuration

All settings live in [config.json](./config.json):

| Field | Default | Description |
|---|---|---|
| `username` | — (required) | Your GitHub username. Which account's followers are scanned. |
| `threshold` | `20000` | Users following at least this many accounts are treated as bots. |
| `whitelist_file` | `whitelist.txt` | File with one username per line, `#` for comments. Never blocked. |
| `blacklist_file` | `blacklist.txt` | File with one username per line, `#` for comments. Blocked immediately. |
| `concurrency` | `10` | Parallel lookups/blocking. |
| `timeout_sec` | `60` | Overall run timeout. Raise it for large follower lists. |
| `dry_run` | `false` | `true` = report what *would* be blocked, block nothing. **Try this first.** |
| `data_dir` | `data` | Where `blocked.json` and daily reports are stored (committed to the repo). |
| `report.issue` | `false` | Open a GitHub issue when new users are blocked. |
| `report.issue_repo` | — | Repo for the issue, e.g. `alice/github-antibot`. |
| `schedule.cron` | `0 0 * * *` | Informational only — the cron in `.github/workflows/antibot.yaml` is what GitHub actually runs. Keep the two in sync. |

Usernames in whitelist/blacklist are matched case-insensitively.

The token (`GH_PAT`) is **not** read from any config file — it comes from the environment, so it never lands in the repository.

### Safety first

Start with `"dry_run": true`, run the workflow once, and review the report to
see exactly who would be blocked before letting it block anyone. Blocks are
one-sided and silent: the blocked user is not notified and cannot unblock
themselves, so check the whitelist regularly.

## Reporting

Every run produces three surfaces:

- **`data/reports/YYYY-MM-DD.md`** — the full daily report, committed to the repo
- **Workflow step summary** — visible on the Actions run page
- **GitHub issue** (if enabled) — opened only when new users were blocked, so you get a notification

`data/blocked.json` holds the persistent record of every blocked user with the
date and reason. It is committed back so past actions stay auditable and
already-blocked users are never examined twice.

## PAT Configuration

Create a [Personal Access Token](https://github.com/settings/personal-access-tokens) (fine-grained) with:

- **Repository permissions:** `Contents` (read and write, for the keep-alive/data commits), `Issues` (read and write, only if `report.issue` is enabled)
- **Account permissions:** `Blocking users` (read and write), `Followers` (read-only)

The token must belong to the same account as `username` in `config.json` — the
blocking API only ever acts as the token owner.

## Keep-Alive Mechanism

GitHub Actions may disable scheduled workflows on inactive repositories. Each run refreshes the `.keep_alive` file and commits the new report/data, keeping the daily schedule alive.