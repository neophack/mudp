// Bounded async iterators that feed a streaming upload queue.
//
// The two adapters here are the memory side of "don't crash on a
// million-file folder": none of them ever materialize the whole file list.
//   - makeFileIterator   : wraps an already-lazy FileList/array (input picks).
//   - makeDropIterator   : walks a dropped folder behind a tiny bounded buffer so
//                          traversal and upload run concurrently and only ~one
//                          batch of File references is held at a time.
//
// All return an async iterator (have [Symbol.asyncIterator]) so a consumer can
// write `for await (const entry of it)` or call .next() directly.

// makeFileIterator adapts a sync iterable (an array of { file, relPath }, or a
// FileList of plain Files) into the async-iterator shape the upload queue pulls
// from. For <input multiple>/<input webkitdirectory> the browser hands a
// FileList whose entries are already lazy File references (contents are not read
// until upload), so iterating it has no memory cost.
export function makeFileIterator(list) {
  let i = 0;
  return {
    next() {
      if (i >= list.length) return Promise.resolve({ done: true, value: undefined });
      const e = list[i++];
      const entry = e && e.file ? e : { file: e, relPath: e.webkitRelativePath || e.name };
      return Promise.resolve({ done: false, value: entry });
    },
    [Symbol.asyncIterator]() { return this; },
  };
}

// makeDropIterator drives a folder walk behind a tiny bounded buffer so the
// upload drains entries at the speed of the wire. The walker (producer) calls
// into a queue via the returned enqueue gate; the upload loop (consumer) pulls
// from the iterator. When the buffer fills, the walker awaits and pauses — so
// only ~1 batch of File references is ever materialized, even for a folder with
// millions of files.
//
// `walkFn(entry, prefix, push)` does the actual FileSystemEntry recursion; it is
// injected so this module has no DOM dependency and is trivially testable. `push`
// may return a Promise the walker awaits (the back-pressure gate).
//
// Producer and consumer wait on DIFFERENT conditions, so they need separate
// waiter sets (a single shared waiter would deadlock: an enqueue below the cap
// never wakes a consumer parked on an empty buffer).
export function makeDropIterator(itemList, walkFn, bufferCap = 10) {
  const queue = [];
  let done = false;
  // producerWaiters: walker parked because the buffer was full. Resumed once the
  //   consumer drains a slot (queue.length < bufferCap).
  // consumerWaiters: upload parked because the buffer was empty. Resumed once the
  //   walker enqueues a slot or signals done.
  const producerWaiters = [];
  const consumerWaiters = [];
  const wakeConsumers = () => { while (consumerWaiters.length) consumerWaiters.shift()(); };
  const wakeProducers = () => { while (producerWaiters.length && queue.length < bufferCap) producerWaiters.shift()(); };

  const enqueue = (entry) => {
    if (entry == null) return Promise.resolve();
    queue.push(entry);
    wakeConsumers(); // a waiting upload can now pull this entry.
    if (queue.length >= bufferCap) return new Promise((resolve) => producerWaiters.push(resolve));
    return Promise.resolve();
  };
  const signalDone = () => { done = true; wakeConsumers(); };

  // A DataTransferItemList is only valid while the drop event is dispatching:
  // the browser clears it as soon as the handler returns to the event loop, so
  // a lazy read after the first await sees an empty list and only the first
  // dropped file would upload. Snapshot every entry synchronously here — the
  // FileSystemEntry / File references themselves stay usable afterwards and
  // remain lazy (no contents read), so the bounded-memory property is untouched.
  const snapshot = [];
  for (let i = 0; i < itemList.length; i++) {
    const item = itemList[i];
    if (item?.kind !== "file") continue;
    const entry = item.webkitGetAsEntry?.();
    if (entry) { snapshot.push({ walk: true, value: entry }); continue; }
    const file = item.getAsFile?.();
    if (file) snapshot.push({ walk: false, value: { file, relPath: file.name } });
  }

  // Kick the walk off in the background; it self-paces via enqueue's gate.
  (async () => {
    try {
      for (const { walk, value } of snapshot) {
        if (walk) await walkFn(value, "", enqueue);
        else await enqueue(value);
      }
    } finally {
      signalDone();
    }
  })();

  return {
    next() {
      if (queue.length) {
        const value = queue.shift();
        wakeProducers(); // free the walker if it was parked on a full buffer
        return Promise.resolve({ done: false, value });
      }
      if (done) return Promise.resolve({ done: true, value: undefined });
      // Buffer empty but walk not finished: wait for the next entry or done.
      return new Promise((resolve) => {
        consumerWaiters.push(() => {
          if (queue.length) {
            const value = queue.shift();
            wakeProducers();
            resolve({ done: false, value });
          } else if (done) {
            resolve({ done: true, value: undefined });
          }
        });
      });
    },
    [Symbol.asyncIterator]() { return this; },
  };
}
