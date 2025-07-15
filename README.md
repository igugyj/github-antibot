# Block bot abusers

Tired of spammy notifications from mass-following bot accounts? This repository contains a GitHub Action that automatically blocks suspicious users who follow you — particularly those who follow tens of thousands of other accounts just to boost their own follower count.

This action runs daily and checks your followers. If a user is following more than a defined threshold (default: 20,000), the action considers them likely a bot and blocks them. These accounts typically provide no value and clutter your followers list.

To use this action, you'll need to create a [**Github PAT token**](https://github.com/settings/personal-access-tokens) with the following permissions:
- Read-only - Followers
- Read and write - Block another user

Add this token as a repository secret named `GH_PAT`.

The action supports two configurable parameters:
- `ANTIBOT_THRESHOLD`: Number of people a user must be following to be considered a bot (default: 20000)
- `ANTIBOT_WHITELIST`: Comma-separated list of usernames to exclude from blocking, even if they exceed the threshold

GitHub Actions may stop running scheduled workflows for inactive repositories. To prevent this, each pipeline run updates the `.keep_alive` file with a new UUID and commits the change, ensuring continued activity and keeping the workflow alive over time.
