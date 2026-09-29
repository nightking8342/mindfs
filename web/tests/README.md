# Session virtualization browser regression

Start `npm run dev` in `web`, then open `/tests/session-virtualization.html`.
Run `await window.runVirtualizationChecks()` in the browser console on a fresh page.
It throws on failure and returns measured row/request counts on success.

The fixture renders the real SessionViewer with 3,000 synthetic tool calls and
stubs detail requests. It checks bounded mounting, initial tail positioning,
search jumps, lazy detail loading, expansion/detail retention across unmounts,
reading position during append, reopening, tail following, short sessions,
large diff expansion, pending question retention, and default user-shell expansion. No live agent
or session is required. The fixture is not a production build entry point.
