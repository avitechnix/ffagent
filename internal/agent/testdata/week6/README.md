Recorded ESPN data for `TestReplayWeek6`: the players in a real week 6 (2026)
matchup, from ESPN's public player data (`leaguedefaults/3`, `kona_playercard`)
and pro schedule, trimmed to what the agent reads.

- `reference.json`: every weekly projection and result, as the public data has it.
- `playercard.json`, `league.json`: the same players the way a private league
  returns them, with only the latest weekly projections (weeks 5-6).
- Week 6 points, and `scoreboard.json`'s game clocks, are as ESPN's app showed
  them at 12:23 CT on Sunday. The Patriots D/ST projection is the value from
  that moment (6.698); ESPN revised it during the game.

Team names and the league itself are made up; no cookies or private league data.
