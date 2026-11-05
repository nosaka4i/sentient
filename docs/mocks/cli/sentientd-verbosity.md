# sentientd --verbosity

**Status:** built (`sentientd --verbosity`, `--policy-file`; and the disk-full pause, `--disk-high-threshold` and `--disk-low-threshold`). Design: [capture-verbosity.md](../../capture-verbosity.md). Health page: `.mocks/health-02.html`.

## The flag

```
sentientd --tetragon --verbosity essential
```

`--verbosity essential|extended|complete`, default `essential`. Same names as `sen policy --verbosity`. An unknown value stops sentientd before it opens anything:

```
$ sentientd --verbosity detailed
sentientd: unknown verbosity "detailed" (use essential, extended or complete)
```

## Startup log

First start on a database that predates the setting (recorded at complete):

```
level=INFO msg="capture level" level=essential previous="complete (before this setting)" source=flag
```

Start at the same level as last time:

```
level=INFO msg="capture level" level=essential source=flag
```

Level changed since the last start:

```
level=INFO msg="capture level changed" from=complete to=essential source=flag
```

Each of these appends a row to `capture_levels` (time, level, source). Nothing already stored is touched.

## The policy does not match

sentientd cannot see the installed policy's level directly. It reads the policy file's `# Verbosity:` header (default `/etc/tetragon/tetragon.tp.d/sentient.yaml`, readable by everyone). If it differs from the flag:

```
level=WARN msg="policy level differs" policy=complete sentientd=essential hint="sen policy --verbosity essential, install it, restart Tetragon"
```

sentientd still filters at its own level, so the database does not grow past what was asked. Health shows the same message (see the mock).

## Disk full

Two flags, named like kubelet's `--image-gc-high-threshold` and `--image-gc-low-threshold`, each a percent of the data directory's filesystem used:

- `--disk-high-threshold <percent>` (default 80): recording pauses here. Health warns 10 points before it (70 by default).
- `--disk-low-threshold <percent>` (default 5 below the high one, 75): recording resumes here.

```
sentientd --disk-high-threshold 80 --disk-low-threshold 75
```

At the high threshold sentientd pauses recording: kernel events are dropped, transcript ingest waits (it resumes from where it stopped, so nothing is lost), and attribution and merge wait. Nothing is deleted.

```
level=ERROR msg="recording paused: disk 81% used" high=80 low=75 free="11 GB" hint="free space; recording resumes at 75% used"
level=INFO  msg="recording resumed" used=74 paused_for=2h14m
```

It resumes at the low threshold, so it does not flap around the high one. The pause and the resume are one gap in `monitor_gaps` (reason "disk 81% used: recording paused"), so the pages show that nothing was recorded then.

Started above the threshold, sentientd starts paused and says so:

```
level=ERROR msg="recording paused: disk 86% used" high=80 low=75 free="8.1 GB" hint="free space; recording resumes at 75% used"
```

A value out of range stops sentientd before it opens anything:

```
$ sentientd --disk-high-threshold 100
sentientd: --disk-high-threshold must be between 10 and 99 (percent of the disk used)

$ sentientd --disk-high-threshold 80 --disk-low-threshold 85
sentientd: --disk-low-threshold must be between 1 and 79 (below --disk-high-threshold 80)
```
