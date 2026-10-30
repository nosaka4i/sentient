# sentientd --verbosity

**Status:** mock, not yet built. Design: [capture-verbosity.md](../../capture-verbosity.md). Health page: `.mocks/health-02.html`.

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

```
level=ERROR msg="recording stopped: disk 95% used" free="2.9 GB" hint="free space or run sudo sentientd prune"
level=INFO  msg="recording resumed" free="4.1 GB"
```

The stop and the resume are written as gaps.
