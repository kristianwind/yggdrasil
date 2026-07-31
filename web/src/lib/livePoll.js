// livePoll runs fn on an interval so a view reflects reality without the user
// reaching for reload.
//
// Two things it does that a bare setInterval does not:
//
// Backgrounded tabs stop polling. A panel left open on a second monitor
// otherwise keeps asking a server for status forever, which is pure load for
// nobody's benefit. It resumes on the next visibility change with an immediate
// tick, so a tab that has been hidden for an hour is current the moment it is
// looked at rather than up to one interval later.
//
// Ticks never overlap. fn is awaited, so a slow response delays the next tick
// instead of stacking requests on a panel that is already struggling — which is
// exactly when a status view matters most.
//
// Returns a stop function; call it from onDestroy.
export function livePoll(fn, ms = 5000) {
  let timer = null;
  let running = false;
  let stopped = false;

  async function tick() {
    if (running || stopped || document.hidden) return;
    running = true;
    try {
      await fn();
    } catch {
      // A failed poll is not an event: the view simply keeps its last known
      // state until the next one succeeds. Reporting it would turn one flaky
      // network moment into a toast every few seconds.
    } finally {
      running = false;
    }
  }

  function start() {
    if (timer || stopped) return;
    timer = setInterval(tick, ms);
  }
  function pause() {
    clearInterval(timer);
    timer = null;
  }
  function onVisibility() {
    if (document.hidden) {
      pause();
    } else {
      tick();
      start();
    }
  }

  document.addEventListener("visibilitychange", onVisibility);
  start();

  return () => {
    stopped = true;
    pause();
    document.removeEventListener("visibilitychange", onVisibility);
  };
}
