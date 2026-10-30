# sen audit

**Status:** mockup, not yet built. Backlog `71372a4`.

Checks that sentient's numbers add up. It reads the database the way the pages do and recounts everything a second, independent way; every number on a page should be reproducible here. Exit status is 0 when nothing is a defect and 1 when something is, so it can run in a test or a cron job. A finding that is explained (a lost kernel event, a command that was never found) is reported but is not a defect; a number that does not add up, or a process no bucket accounts for, is.

```
$ sen audit
Audited Oct 25 04:45 to Oct 30 02:10 · 5,271 processes · 21,840 logged actions · 836,764 kernel events
Capture level: complete (since Oct 25 04:45, one level for the whole period)

PROCESSES UNDER AN AGENT                          5,102
  confirmed (a logged command ran)                  730
  unreported (no logged call explains it)            9
  background commands                            3,301
  forks and stubs (not commands)                  1,018
  subprocesses of a command                          44
  pending (the log has not caught up)                 0
  accounted for in no bucket                          0     ok

LOGGED ACTIONS, MACHINE KINDS                    21,840
  confirmed                                       4,962
  from before the kernel was recording           16,850     (Oct 25 04:45 is the first kernel event)
  unverified                                         3
    kernel record missing (probable capture loss)    1     prompt 5088: cut -d: -f1
    command not found                                1     prompt 5066: python3 -
    not approved                                     1     prompt 5072: rm -rf build
    matching missed                                  0     ok
  not checked (web fetch)                           82

PROMPT TILES                                        812 prompts recomputed
  agent log = confirmed + unverified + not checked + pending      812 of 812 ok
  kernel log = confirmed + unreported                             812 of 812 ok

DEFECTS 0

  sen audit --prompt <id>     the same checks for one prompt, with every action listed
  sen audit --since <date>    only prompts and processes after a date
  sen audit --json            machine-readable, for tests and dashboards
```

A prompt with a finding:

```
$ sen audit --prompt 5088
Prompt 5088 · Oct 30 01:50:37 to 01:53:11 · capture level complete

  agent log 49 = confirmed 48 + unverified 1 + not checked 0 + pending 0     ok
  kernel log 48 = confirmed 48 + unreported 0                                 ok

UNVERIFIED (1)
  01:51:25  cut -d: -f1       kernel record missing (probable capture loss)
            the call matched its shell, 3 of its 4 other commands are confirmed, the result succeeded
            (it printed "main at 470"); the raw kernel stream has no exec between 01:51:28.185 and .255
```

**What it reports and what it does not.** Unverified is split by reason so a lost event is not confused with a matching bug, and a command that never ran is not confused with either. "Matching missed" is the one reason that is always a defect: it means a logged command has a process under its shell that the matching failed to pair. The reasons come from the call's wrapper (matched or not), the result the agent logged (success, error, not found, refused) and the other commands in the same call.

**Capture level.** The header states the verbosity the period was recorded at, read from the `capture_levels` table. At `essential`, opens of build caches and similar are not recorded, and the audit expects none; seeing them there is reported as a level mismatch. A change of level in the period is listed with its time.
