# fast-agent-sim

What would it feel like if coding agents answered in a second instead of a minute? This toy lets you find out by replaying your own Claude Code and Codex sessions at roughly ten times the speed.

## What it does

Pick a session and watch it again: the prompt is typed out at human speed, you press Return, and the agent's turn lands almost at once - a thinking beat, tool calls ticking past, diffs, prose streaming out. Long thinks are squeezed into short dramatic pauses, and everything else runs at 10x.

Files the agent wrote with Write or Edit really land on disk in a temp directory as the replay runs, and you get the path at the end so you can go and poke them.

A few demo sessions are built in, for when you want to show it off without any sessions of your own - in the Haido demo the "agent" builds a small deduction game, and the game really plays.

## Installing

Download the binary for your machine from the [releases page](https://github.com/ohnotnow/fast-agent-sim-go/releases), then:

```bash
chmod +x fas-darwin-arm64
mv fas-darwin-arm64 ~/bin/fas
```

On a Mac, a downloaded binary is quarantined and macOS will refuse to run it. Clear that with:

```bash
xattr -d com.apple.quarantine ~/bin/fas
```

Or, with Go installed:

```bash
go install github.com/ohnotnow/fast-agent-sim-go/cmd/fas@latest
```

## Watching a session

Run it from inside a project you've used Claude Code or Codex in:

```bash
fas
```

You get this project's sessions, newest first, with the demos underneath. Press Enter to play the highlighted one, so "show me that last session at 10x" is `fas` then Enter. Use `/` to filter by what you typed in the session, and `j`/`k` or the arrow keys to move.

While a session plays, Return sends each prompt. Type `q` then Return, or press Ctrl-C, to stop.

To look at another project's sessions without going there:

```bash
fas -project ~/code/my_app   # a path
fas -project my_app          # or just a name
```

You can also play a transcript file directly:

```bash
fas play ~/.claude/projects/-Users-you-code-my-app/some-session.jsonl
```

## Showing off in meetings

```bash
fas demo -auto -loop
```

This plays the built-in demos one after another without anyone pressing Return, and keeps going until Ctrl-C. Leave out `-loop` to play them once, name one to play just that (`fas demo haido`), or leave out `-auto` to press Return yourself and control the pace.

## Making a new demo

Found a session that's worth showing other people?

```bash
fas export my-demo
```

Pick the session and it's saved as `my-demo.jsonl` in the current directory. Check it with `fas play my-demo.jsonl`. To ship it with the binary, copy it into `internal/fas/demos/` and rebuild.

Export rewrites the project path to `.`, your home directory to `~` and your username to `user`, then refuses to save if anything machine-specific is still left. It won't catch everything, though. The file contains whatever the session contained - file contents, command output, everything you typed - so read it before you share it.

## Building

```bash
go build -o fas ./cmd/fas
go test ./...
```

Releases are built by GitHub Actions when a `v*` tag is pushed.
