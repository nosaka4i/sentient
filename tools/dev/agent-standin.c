// agent-standin: a stand-in for a coding agent, for measuring what the Mac
// Endpoint Security extension records, without a real agent or a model.
//
// The extension treats any process whose executable path contains
// "/claude-code/" as an agent. Build this program into such a folder and run
// it with one shell command as its argument; the command runs as its child,
// so the extension records the child's activity the way it would for an agent.
//
//   mkdir -p ~/cctest/claude-code
//   cc -o ~/cctest/claude-code/agent-standin tools/dev/agent-standin.c
//   cd <the folder to run in>
//   ~/cctest/claude-code/agent-standin 'go test ./...'
//
// It has to be a compiled program: a shell script would run as /bin/sh, which
// the extension does not match. Mac only; for the prototype in apps/mac/prototype.

#include <stdio.h>
#include <stdlib.h>
#include <sys/wait.h>

int main(int argc, char **argv) {
    if (argc != 2) {
        fprintf(stderr, "usage: %s '<shell command>'\n", argv[0]);
        return 2;
    }
    // system() forks /bin/sh -c "<command>" and waits, so the stand-in stays
    // the parent of everything the command starts.
    int status = system(argv[1]);
    if (status == -1) {
        perror("system");
        return 1;
    }
    if (WIFEXITED(status)) {
        return WEXITSTATUS(status);
    }
    return 1;
}
