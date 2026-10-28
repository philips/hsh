# Manual terminal checks

The inline review (status line, Ctrl+Space review screen, pause) writes
escape sequences straight to the user's terminal, so how a terminal treats
scroll regions, scrollback, bracketed paste, and keyboard protocols matters.
The automated tests use libghostty's terminal core as the stand-in terminal;
this checklist covers real terminals. Run it per terminal and fill in the
table at the bottom.

## Setup

- Build with the emulator: `GOEXPERIMENT=simd CGO_ENABLED=0 go build -o hsh .`
  and confirm `./hsh version` prints `terminal emulator: libghostty-vt`.
- Use an image that reaches a bash prompt, for example
  `./hsh run localhost/pi` (or any image with an `org.ifup.hsh` label), and
  keep `monitor.enabled` and `monitor.status_line` at their defaults.
- To get something to review, run `curl -s -m 8 https://blocked.example.test`
  in an `allowlist`-mode session (the pi image is one). The count in the
  status line goes up within about two seconds.
- In the terminal, note the version and whether it is run directly, inside
  tmux or screen, or over ssh.

## Checks

Mark each pass, fail, or n/a, and describe any failure.

### Status row and scroll region

1. The bottom row is a green status line that reads `proxy: nothing to
   review` and stays at the bottom while you type. After a request is denied
   (see Setup) it turns blue and counts requests; opening the review screen
   turns it red.
2. `seq 1 200`: the output scrolls above the status line; the status line is
   never overwritten or duplicated.
3. Scroll back with the terminal's own scrollback (mouse wheel, Shift+PgUp,
   or the terminal's scrollback key): all 200 numbers are there, in order,
   with no gaps, no repeated status lines in the middle, and no blank stretch.
4. `clear`: the screen clears and the status line comes back within a
   moment (a brief flicker is expected).
5. Full-screen programs: `less /etc/services`, `vim`, `htop` (if installed).
   The program fills the area above the status line, the status line is
   intact while it runs, and the session looks right again after you quit.
6. Resize the terminal window larger and smaller while at a prompt and
   while in `less`: the status line follows the bottom edge, nothing is left
   behind, and `stty size` inside the session reports one row fewer than the
   window.

### Review screen

7. Ctrl+Space (with something pending): the whole screen switches to the
   `hsh monitor` view and the status line turns red; Ctrl+Space again,
   `q`, or Esc closes it and the session screen is exactly as it was.
8. With the review screen open, Ctrl+C closes it and does not interrupt
   whatever is running in the session (try it while `sleep 30` runs, then
   confirm `sleep` is still going).
9. Pause without loss: in the session run
   `for i in $(seq 1 3000); do echo line-$i; sleep 0.002; done`, open the
   review screen while it runs, wait several seconds, close it. The loop
   continues from where it was held, and scrolling back shows consecutive
   numbers with none missing or duplicated.
10. Open the review screen while `less` or `vim` is running: closing it
    restores the full-screen program's display.
11. Select a host with space and approve with enter; the host appears in the
    session's `network.allow`; the status line count drops.
12. Resize the window while the review screen is open: it redraws to the new
    size and closes cleanly.

### Input

13. Paste text (several lines) at the shell prompt: it arrives as pasted text
    and does not run until you press Enter (bracketed paste still works
    after you have opened and closed the review screen).
14. With the review screen open, paste text containing `q`, `a`, and `i`:
    nothing is approved, ignored, or closed. Press `/` first and paste:
    the text lands in the filter.
15. With a program in the session that enables mouse reporting (`vim` with
    `:set mouse=a`, or `htop`): click and scroll in it, then open the review
    screen and click and scroll: nothing happens in the review screen and
    nothing leaks into the session afterwards.
16. Ctrl+] detaches (check this with a program running that enables the
    kitty keyboard protocol, such as the pi harness). `hsh run --resume`
    re-attaches and the screen repaints.
17. Ctrl+Space with the kitty keyboard protocol active (pi running) still
    opens the review screen.

### Colors and attributes

18. The status line is readable (black on green, white on blue, white on red) with your
    color scheme, including a light scheme.
19. The review screen's selection highlight and borders render correctly.

## Recording results

| Terminal (version) | direct / tmux / ssh | Failed checks | Notes |
|---|---|---|---|
| Ghostty | direct | | reference |
| kitty | | | |
| iTerm2 | | | |
| Terminal.app | | | |
| WezTerm | | | |
| Alacritty | | | |
| GNOME Terminal (VTE) | | | |
| Konsole | | | |
| tmux inside Ghostty | tmux | | |

Checks 2 and 3 decide whether the reserved status row can stay the default:
a terminal that drops scrolled lines from its scrollback when a scroll
region excludes the last row fails them, and `monitor.status_line: false`
is the workaround for that terminal.
