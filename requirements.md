# repoman app

A TUI to manage locally cloned git repos

## Language
- Should be an efficient compiled/typed language like go, rust, or zig

## Features
- Configure dirs where local repos are cloned
- List repos, list and grid view
- Show branches, highlight if not on default branch
- Show how far ahead or behind the cloned repo is
- Fetch all repos with r
- Safe-update one/all repos with u/U
- On update, if a repo is on a branch, and that branch was already merged and deleted remotely, switch back to default branch
- Don't update/change branches where not safe to do so (would lose changes, working on a branch etc)
- Exclude repo from update, e.g. ignore one you don't want to pull automatically
- Should have tasteful terminal colors and animations for loading

## Preferences
Needs locally-managed preferences, configurable in the app in settings menu (, shortcut). Preferences should persist in a folder in ~ somewhere user settings normally go.