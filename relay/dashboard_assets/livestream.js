// liveWsPath returns the /surv/ws path segment for a channel's live stream:
// the base (sub) stream, or the "<base>@main" high-res stream only when the
// channel records hi-res AND the player's HD toggle is on.
function liveWsPath(basePath, hires, hdOn) {
  return (hires && hdOn) ? basePath + '@main' : basePath;
}
if (typeof module !== 'undefined' && module.exports) { module.exports = { liveWsPath }; }
