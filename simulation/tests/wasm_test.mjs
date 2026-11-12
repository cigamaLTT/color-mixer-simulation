import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// Deterministic Chips API host for the compiled custom-chip WASM.
const simulationDir = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const memory = new WebAssembly.Memory({ initial: 32 });
let instance;
let simUs = 0;
let nextPin = 1;
let nextAttr = 1;
let nextTimer = 1;
let framebuffer;
const pins = new Map();
const pinNames = new Map();
const attrs = new Map();
const attrNames = new Map();
const timers = new Map();
const view = () => new DataView(memory.buffer);
const string = (ptr) => {
  const bytes = new Uint8Array(memory.buffer);
  let end = ptr;
  while (bytes[end]) end++;
  return new TextDecoder().decode(bytes.subarray(ptr, end));
};
function addAttr(ptr, value) {
  const id = nextAttr++;
  attrs.set(id, value);
  attrNames.set(string(ptr), id);
  return id;
}
const env = {
  memory,
  pinInit(ptr, mode) {
    const id = nextPin++;
    pins.set(id, { mode, value: mode === 17 || mode === 2 ? 1 : 0, voltage: 0 });
    pinNames.set(string(ptr), id);
    return id;
  },
  pinWatch(id, ptr) {
    pins.get(id).watch = { data: view().getUint32(ptr, true),
      edge: view().getUint32(ptr + 4, true), fn: view().getUint32(ptr + 8, true) };
    return 1;
  },
  pinWrite(id, value) { pins.get(id).value = value; },
  pinDACWrite(id, value) { pins.get(id).voltage = value; return value; },
  attrInit: addAttr,
  attrInitFloat: addAttr,
  attrRead(id) { return attrs.get(id); },
  attrReadFloat(id) { return attrs.get(id); },
  framebufferInit(widthPtr, heightPtr) {
    view().setUint32(widthPtr, 760, true);
    view().setUint32(heightPtr, 520, true);
    return 1;
  },
  timerInit(ptr) {
    const id = nextTimer++;
    timers.set(id, { data: view().getUint32(ptr, true),
      fn: view().getUint32(ptr + 4, true), at: Infinity });
    return id;
  },
  timerStart(id, delay, repeat) {
    Object.assign(timers.get(id), { at: simUs + delay, delay, repeat });
  },
  timerStop(id) { timers.get(id).at = Infinity; },
  getSimNanos() { return simUs * 1000; },
  bufferWrite(id, offset, ptr, size) {
    assert.equal(id, 1);
    assert.equal(offset, 0);
    assert.equal(size, 760 * 520 * 4);
    framebuffer = Buffer.from(memory.buffer, ptr, size);
  },
};
({ instance } = await WebAssembly.instantiate(readFileSync(resolve(simulationDir, 'build/mixer.chip.wasm')), { env }));
const table = instance.exports.__indirect_function_table;
instance.exports.chipInit();
assert.equal(instance.exports.__wokwi_api_version_1(), 1);

function tickTo(target) {
  assert(target >= simUs);
  for (;;) {
    let event;
    for (const timer of timers.values())
      if (timer.at <= target && (!event || timer.at < event.at)) event = timer;
    if (!event) break;
    simUs = event.at;
    if (event.repeat) event.at += event.delay;
    else event.at = Infinity;
    table.get(event.fn)(event.data);
  }
  simUs = target;
}
function setPin(name, high) {
  const id = pinNames.get(name);
  const pin = pins.get(id);
  if (pin.value === +high) return;
  pin.value = +high;
  const { watch } = pin;
  if (watch) table.get(watch.fn)(watch.data, id, +high);
}
function attr(name, value) {
  assert(attrNames.has(name), `unknown control ${name}`);
  attrs.set(attrNames.get(name), value);
}
function digital(name) { return pins.get(pinNames.get(name)).value; }
function volts(name) { return pins.get(pinNames.get(name)).voltage; }
function readScale() {
  const started = simUs;
  while (digital('HX_DOUT')) {
    tickTo(simUs + 1000);
    assert(simUs - started < 200000, 'HX711 conversion timeout');
  }
  let word = 0;
  for (let bit = 0; bit < 24; bit++) {
    setPin('HX_SCK', true);
    tickTo(simUs + 2);
    word = (word << 1) | digital('HX_DOUT');
    setPin('HX_SCK', false);
    tickTo(simUs + 2);
  }
  setPin('HX_SCK', true);
  tickTo(simUs + 2);
  assert.equal(digital('HX_DOUT'), 1, '25th clock starts next conversion');
  setPin('HX_SCK', false);
  tickTo(simUs + 2);
  return (word << 8) >> 8;
}

assert(Math.abs(volts('FLOAT_GREEN') - 3.3) < 0.00001);
assert.equal(readScale(), 120000);
setPin('PUMP_GREEN', true);
setPin('STIR_GREEN', true);
tickTo(simUs + 3000000);
setPin('PUMP_GREEN', false);
setPin('STIR_GREEN', false);
// Consume a possibly old latched sample, then verify the next fresh conversion.
readScale();
assert.equal(readScale(), 180000);
assert(Math.abs(volts('FLOAT_GREEN') - 3.102) < 0.0001);
setPin('PUMP_SKIN', true);
setPin('STIR_SKIN', true);
tickTo(simUs + 2000000);
setPin('PUMP_SKIN', false);
setPin('STIR_SKIN', false);
readScale();
assert.equal(readScale(), 220000);
setPin('PUMP_BLUE', true);
setPin('STIR_BLUE', true);
tickTo(simUs + 5000000);
setPin('PUMP_BLUE', false);
setPin('STIR_BLUE', false);
setPin('STIR_CUP', true);
readScale();
assert.equal(readScale(), 320000);
tickTo(simUs + 50000);
writeFileSync(resolve(simulationDir, 'build/preview.rgba'), framebuffer);

attr('blockGreen', 1);
tickTo(simUs + 50000);
setPin('PUMP_GREEN', true);
tickTo(simUs + 1000000);
setPin('PUMP_GREEN', false);
readScale();
assert.equal(readScale(), 320000, 'blocked pump cannot transfer liquid');
attr('emptyBlue', 1);
tickTo(simUs + 50000);
assert.equal(volts('FLOAT_BLUE'), 0);
attr('emptyBlue', 0);
tickTo(simUs + 50000);
assert.equal(volts('FLOAT_BLUE'), 0, 'release empty does not refill');
attr('refillSources', 1);
tickTo(simUs + 50000);
assert(Math.abs(volts('FLOAT_BLUE') - 3.3) < 0.00001);

attr('scaleNoiseG', 1);
tickTo(simUs + 50000);
let noisy = [];
for (let i = 0; i < 5; i++) noisy.push(readScale());
assert(noisy.every(count => count >= 319000 && count <= 321000));
assert(new Set(noisy).size > 1, 'noise must vary between conversions');
attr('disconnectScale', 1);
tickTo(simUs + 200000);
assert.equal(digital('HX_DOUT'), 1);
attr('disconnectScale', 0);
attr('scaleNoiseG', 0);
tickTo(simUs + 200000);
assert.equal(readScale(), 320000);

attr('drainReceiver', 1);
tickTo(simUs + 50000);
readScale();
assert.equal(readScale(), 120000, 'draining receiver keeps cup tare');
console.log('PASS: actual WASM GPIO pump/stirrer inputs, 3 float DAC outputs, 25-clock HX711 gross samples, block/empty/refill/noise/disconnect/drain controls and framebuffer');
