/* Wokwi plant model only. The ESP32 firmware is Rust in firmware/. */
#include "wokwi-api.h"
#include "hx711_model.h"
#include "mixer_model.h"
#include "mixer_render.h"

typedef struct {
  mixer_model_t model;
  hx711_model_t hx;
  pin_t pump[3], stirrer[4], level[3], dout, sck;
  timer_t tick_timer, sample_timer, power_timer;
  buffer_t framebuffer;
  uint32_t empty_attr[3], block_attr[3];
  uint32_t refill_attr, drain_attr, noise_attr, disconnect_attr, flow_attr, cup_attr;
  uint32_t last_refill, last_drain, noise_mg, random_state, frame;
  uint64_t last_update_us;
  int32_t sample_raw;
  bool disconnected;
} chip_state_t;

static chip_state_t chip;
static uint32_t display_pixels[MIXER_DISPLAY_WIDTH * MIXER_DISPLAY_HEIGHT];

static uint64_t now_us(void) { return get_sim_nanos() / 1000u; }

static void advance(chip_state_t *state, uint64_t now) {
  uint64_t elapsed = now - state->last_update_us;
  /* Split long pauses so the deterministic core never truncates a time delta. */
  while (elapsed) {
    const uint32_t step = elapsed > 1000000u ? 1000000u : (uint32_t)elapsed;
    mixer_step(&state->model, step);
    elapsed -= step;
  }
  state->last_update_us = now;
}

static void sync_levels(chip_state_t *state) {
  for (unsigned i = 0; i < 3; ++i)
    pin_dac_write(state->level[i], mixer_level_voltage(&state->model, i));
}

static void controls(chip_state_t *state) {
  uint8_t empty = 0, blocked = 0;
  for (unsigned i = 0; i < 3; ++i) {
    if (attr_read(state->empty_attr[i])) empty |= 1u << i;
    if (attr_read(state->block_attr[i])) blocked |= 1u << i;
  }
  mixer_force_empty(&state->model, empty);
  state->model.blocked = blocked;
  uint32_t flow = attr_read(state->flow_attr);
  if (flow > 200) flow = 200;
  state->model.flow_ul_s = flow * 1000u;
  uint32_t cup = attr_read(state->cup_attr);
  if (cup > 2000) cup = 2000;
  state->model.cup_mg = cup * 1000u;
  float noise = attr_read_float(state->noise_attr);
  if (noise < 0) noise = 0;
  if (noise > 100) noise = 100;
  state->noise_mg = (uint32_t)(noise * 1000.0f);
  state->disconnected = attr_read(state->disconnect_attr) != 0;
  const uint32_t refill = attr_read(state->refill_attr);
  if (refill != state->last_refill) {
    mixer_refill(&state->model);
    state->last_refill = refill;
  }
  const uint32_t drain = attr_read(state->drain_attr);
  if (drain != state->last_drain) {
    mixer_drain_receiver(&state->model);
    state->last_drain = drain;
  }
}

static int32_t raw_counts(chip_state_t *state, bool new_noise) {
  static int32_t noise;
  if (new_noise) {
    state->random_state = state->random_state * 1664525u + 1013904223u;
    noise = state->noise_mg ?
        (int32_t)(state->random_state % (2u * state->noise_mg + 1u)) -
        (int32_t)state->noise_mg : 0;
  }
  return (int32_t)mixer_gross_mg(&state->model) + noise;
}

static void sync_dout(chip_state_t *state) {
  pin_write(state->dout, state->hx.dout ? HIGH : LOW);
}

static void schedule_conversion(chip_state_t *state, uint64_t now) {
  if (state->hx.ready || state->hx.powered_down || state->disconnected) return;
  uint64_t delay = state->hx.next_ready_us > now ? state->hx.next_ready_us - now : 1u;
  if (delay > HX711_CONVERSION_US) delay = HX711_CONVERSION_US;
  timer_start(state->sample_timer, (uint32_t)delay, false);
}

static void conversion_done(void *user_data) {
  chip_state_t *state = user_data;
  const uint64_t now = now_us();
  advance(state, now);
  controls(state);
  state->sample_raw = raw_counts(state, true);
  hx711_update(&state->hx, now, state->sample_raw, state->disconnected);
  sync_dout(state);
  schedule_conversion(state, now);
}

static void power_down_check(void *user_data) {
  chip_state_t *state = user_data;
  hx711_update(&state->hx, now_us(), state->sample_raw, state->disconnected);
  sync_dout(state);
}

static void pin_changed(void *user_data, pin_t pin, uint32_t high) {
  chip_state_t *state = user_data;
  const uint64_t now = now_us();
  advance(state, now);
  if (pin == state->sck) {
    const bool was_ready = state->hx.ready;
    const bool was_powered_down = state->hx.powered_down;
    hx711_clock(&state->hx, now, high != 0, state->sample_raw, state->disconnected);
    sync_dout(state);
    if (high) timer_start(state->power_timer, HX711_POWER_DOWN_US, false);
    else timer_stop(state->power_timer);
    if ((was_ready && !state->hx.ready) ||
        (was_powered_down && !state->hx.powered_down)) schedule_conversion(state, now);
  } else {
    for (unsigned i = 0; i < 3; ++i) {
      if (pin == state->pump[i]) {
        if (high) state->model.pumps |= 1u << i;
        else state->model.pumps &= ~(1u << i);
      }
    }
    for (unsigned i = 0; i < 4; ++i) {
      if (pin == state->stirrer[i]) {
        if (high) state->model.stirrers |= 1u << i;
        else state->model.stirrers &= ~(1u << i);
      }
    }
  }
}

static void plant_tick(void *user_data) {
  chip_state_t *state = user_data;
  const uint64_t now = now_us();
  advance(state, now);
  const bool was_disconnected = state->hx.disconnected;
  controls(state);
  /* Only the conversion timer may latch a sample. In particular, a plant tick
   * at the same timestamp must not win the race with stale cached noise. */
  if (was_disconnected != state->disconnected)
    hx711_update(&state->hx, now, state->sample_raw, state->disconnected);
  sync_dout(state);
  if (was_disconnected && !state->disconnected) schedule_conversion(state, now);
  sync_levels(state);
  mixer_render_state_t view = {
    .frame_number = state->frame++,
    .noise_mg = state->noise_mg,
    .measured_mg = state->sample_raw,
    .scale_disconnected = state->disconnected
  };
  mixer_render(display_pixels, &state->model, &view);
  buffer_write(state->framebuffer, 0, display_pixels, sizeof display_pixels);
}

void chip_init(void) {
  chip_state_t *state = &chip;
  mixer_init(&state->model);
  state->random_state = 0x434f4c52u;
  state->last_update_us = now_us();
  hx711_init(&state->hx, state->last_update_us);
  state->sample_raw = MIXER_DEFAULT_CUP_MG;
  pin_init("VCC", INPUT);
  pin_init("GND", INPUT);
  const char *pump_names[] = {"PUMP_GREEN", "PUMP_SKIN", "PUMP_BLUE"};
  const char *stir_names[] = {"STIR_GREEN", "STIR_SKIN", "STIR_BLUE", "STIR_CUP"};
  const char *float_names[] = {"FLOAT_GREEN", "FLOAT_SKIN", "FLOAT_BLUE"};
  const char *empty_names[] = {"emptyGreen", "emptySkin", "emptyBlue"};
  const char *block_names[] = {"blockGreen", "blockSkin", "blockBlue"};
  const pin_watch_config_t watch = {.edge = BOTH, .pin_change = pin_changed, .user_data = state};
  for (unsigned i = 0; i < 3; ++i) {
    state->pump[i] = pin_init(pump_names[i], INPUT_PULLDOWN);
    pin_watch(state->pump[i], &watch);
    state->level[i] = pin_init(float_names[i], ANALOG);
    state->empty_attr[i] = attr_init(empty_names[i], 0);
    state->block_attr[i] = attr_init(block_names[i], 0);
  }
  for (unsigned i = 0; i < 4; ++i) {
    state->stirrer[i] = pin_init(stir_names[i], INPUT_PULLDOWN);
    pin_watch(state->stirrer[i], &watch);
  }
  state->dout = pin_init("HX_DOUT", OUTPUT_HIGH);
  state->sck = pin_init("HX_SCK", INPUT_PULLDOWN);
  pin_watch(state->sck, &watch);
  state->refill_attr = attr_init("refillSources", 0);
  state->drain_attr = attr_init("drainReceiver", 0);
  state->noise_attr = attr_init_float("scaleNoiseG", 0.0f);
  state->disconnect_attr = attr_init("disconnectScale", 0);
  state->flow_attr = attr_init("flowMlPerSecond", 20);
  state->cup_attr = attr_init("cupMassG", 120);
  state->last_refill = attr_read(state->refill_attr);
  state->last_drain = attr_read(state->drain_attr);
  uint32_t width, height;
  state->framebuffer = framebuffer_init(&width, &height);
  /* The paired chip JSON guarantees 760 x 520 RGBA pixels. */
  if (width != MIXER_DISPLAY_WIDTH || height != MIXER_DISPLAY_HEIGHT) return;
  const timer_config_t tick_config = {.callback = plant_tick, .user_data = state};
  const timer_config_t sample_config = {.callback = conversion_done, .user_data = state};
  const timer_config_t power_config = {.callback = power_down_check, .user_data = state};
  state->tick_timer = timer_init(&tick_config);
  state->sample_timer = timer_init(&sample_config);
  state->power_timer = timer_init(&power_config);
  controls(state);
  sync_levels(state);
  plant_tick(state);
  timer_start(state->tick_timer, 50000, true);
  timer_start(state->sample_timer, HX711_CONVERSION_US, false);
}
