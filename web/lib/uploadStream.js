// Bounded async iterators that feed a streaming upload queue.
//
// The three adapters here are the memory side of "don't crash on a
// million-file folder": none of them ever materialize the whole file list.
//   - makeFileIterator   : wraps an already-lazy FileList/array (input picks).
//   - makeDropIterator   : walks a dropped folder behind a tiny bounded buffer so
//                          traversal and upload run concurrently and only ~one
//                          batch of File references is held at a time.
//   - prependIterator    : pushes a peeked entry back to the head of a stream.
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

// prependIterator returns an async iterator that yields `first` and then
// continues with `rest`. Used to push a peeked entry back to the head of the
// stream when it overflowed the current batch's byte cap.
export function prependIterator(rest, first) {
  let emitted = false;
  const restIter = rest[Symbol.asyncIterator] ? rest[Symbol.asyncIterator]() : rest;
  return {
    next() {
      if (!emitted) { emitted = true; return Promise.resolve({ done: false, value: first }); }
      return Promise.resolve(restIter.next());
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

  // Kick the walk off in the background; it self-paces via enqueue's gate.
  (async () => {
    try {
      for (let i = 0; i < itemList.length; i++) {
        const item = itemList[i];
        if (item.kind === "file") {
          const entry = item.webkitGetAsEntry?.();
          if (entry) { await walkFn(entry, "", enqueue); continue; }
        }
        const file = item.getAsFile?.();
        if (file) await enqueue({ file, relPath: file.name });
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
