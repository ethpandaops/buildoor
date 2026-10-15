// One requestAnimationFrame loop shared by every animated element (current
// time lines, growing build lines, the timeline's slot window). Each
// subscriber gets the frame time; the loop runs only while something
// subscribed, and pauses with the tab (rAF does not fire in background tabs).

type FrameCallback = (now: number) => void;

const callbacks = new Set<FrameCallback>();
let frame = 0;

function tick() {
  const now = Date.now();
  for (const cb of callbacks) cb(now);
  frame = callbacks.size > 0 ? requestAnimationFrame(tick) : 0;
}

// onFrame registers a per-frame callback and returns its unsubscribe.
export function onFrame(cb: FrameCallback): () => void {
  callbacks.add(cb);
  if (!frame) frame = requestAnimationFrame(tick);
  return () => {
    callbacks.delete(cb);
    if (callbacks.size === 0 && frame) {
      cancelAnimationFrame(frame);
      frame = 0;
    }
  };
}
