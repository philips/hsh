# process-approval prototype

Mechanics behind plans/0024-process-approval.md: a broker stops the
terminal's foreground job on a denial, the shell approves over a socket
that a Landlock-guarded agent cannot open, and `fg` resumes the job.

    ./test.sh

Needs Go, python3 (pty driver), and a kernel with Landlock ABI 9
(`LANDLOCK_ACCESS_FS_RESOLVE_UNIX`). Stdlib only; separate module so it
is not built by the repository's `./...`.
