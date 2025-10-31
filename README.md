# GitHub Antibot Action

Tired of spammy notifications from mass-following bot accounts? This GitHub Action automatically blocks suspicious users who follow you, helping to keep your follower list clean and your notifications relevant.

The action runs on a daily schedule, checking your followers. If a user is found to be following an excessive number of other accounts (i.e., above a configurable threshold), they are considered a bot and blocked.

## Usage

You should be able to fork this repository to use this. Just make sure to change the [configuration](#configuration) settings.

## Configuration

The action [antibot.yaml](./.github/workflows/antibot.yaml) is configured using environment variables:

- `GH_PAT`: **Required.** A GitHub Personal Access Token with the necessary permissions to block users and write to the repository. See [PAT Configuration](#pat-configuration) for details.
- `ANTIBOT_THRESHOLD`: The number of accounts a user must be following to be considered a bot. Defaults to `20000`.
- `ANTIBOT_WHITELIST`: A comma-separated list of usernames to exclude from blocking, even if they exceed the threshold.

**Note:** The `GH_USERNAME` is automatically determined from the user running the action (`github.actor`).

## PAT Configuration

You need to create a [GitHub Personal Access Token](https://github.com/settings/personal-access-tokens) with the following permissions:

- **Repository permissions:**
  - `Contents`: Read and write (to update the `.keep_alive` file)
- **Account permissions:**
  - `Blocking users`: Read and write
  - `Followers`: Read-only

Once created, add the token as a secret to your repository with the name `GH_PAT`.

## Keep-Alive Mechanism

GitHub Actions may disable scheduled workflows on inactive repositories. To prevent this, the action updates a `.keep_alive` file with a new timestamp in each run. This small commit ensures the repository remains active, keeping the daily scans running.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
