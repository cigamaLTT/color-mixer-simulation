#include "hx711_model.h"
#include "mixer_model.h"
#include <assert.h>
#include <stdio.h>

static void test_flow_and_feedback(void) {
  mixer_model_t model;
  mixer_init(&model);
  assert(mixer_gross_mg(&model) == 120000);
  assert(mixer_level_voltage(&model, 0) > 3.299f);
  model.stirrers = 15;
  mixer_step(&model, 1000000);
  assert(mixer_net_mg(&model) == 0); /* Stirring cannot create fluid. */
  model.pumps = 1;
  mixer_step(&model, 1000000);
  assert(model.source_ul[0] == 980000);
  assert(model.source_ul[1] == 1000000);
  assert(mixer_net_mg(&model) == 20000);
  assert(mixer_gross_mg(&model) == 140000);
  assert(mixer_level_voltage(&model, 0) > 3.233f &&
         mixer_level_voltage(&model, 0) < 3.235f);
  for (unsigned i = 0; i < 100000; ++i) mixer_step(&model, 10);
  assert(mixer_net_mg(&model) == 40000); /* Sub-microliter timing is conserved. */
  model.pumps = 0;
  mixer_step(&model, 1000000);
  assert(mixer_net_mg(&model) == 40000);
  assert(model.source_ul[0] + model.received_ul[0] == MIXER_TANK_CAPACITY_UL);
  assert(!mixer_has_flow(&model, 99));
}

static void test_faults_refill_and_overload(void) {
  mixer_model_t model;
  mixer_init(&model);
  model.pumps = 7;
  model.blocked = 2;
  mixer_force_empty(&model, 4);
  mixer_step(&model, 1000000);
  assert(model.received_ul[0] == 20000);
  assert(model.received_ul[1] == 0 && model.source_ul[1] == 1000000);
  assert(model.received_ul[2] == 0 && model.source_ul[2] == 0);
  assert(mixer_level_voltage(&model, 2) == 0);
  mixer_refill(&model);
  assert(model.source_ul[0] == 1000000 && model.source_ul[2] == 0);
  mixer_force_empty(&model, 0);
  assert(model.source_ul[2] == 0); /* Releasing empty is not an implicit refill. */
  mixer_refill(&model);
  assert(model.source_ul[2] == 1000000);
  model.pumps = 1;
  model.source_ul[0] = 123;
  mixer_step(&model, 1000000);
  assert(model.received_ul[0] == 20123 && model.source_ul[0] == 0);
  mixer_step(&model, 1000000);
  assert(model.received_ul[0] == 20123); /* Empty cannot produce negative volume. */
  mixer_drain_receiver(&model);
  assert(mixer_gross_mg(&model) == 120000);
  model.received_ul[0] = 880000;
  assert(mixer_gross_mg(&model) == MIXER_SCALE_CAPACITY_MG);
  model.received_ul[0] += 1;
  assert(mixer_gross_mg(&model) > MIXER_SCALE_CAPACITY_MG);
}

static uint32_t read_word(hx711_model_t *hx, uint64_t *time_us, int32_t changing_raw) {
  uint32_t word = 0;
  for (unsigned bit = 0; bit < 24; ++bit) {
    *time_us += 2;
    hx711_clock(hx, *time_us, true, changing_raw, false);
    word = (word << 1) | hx->dout;
    *time_us += 2;
    hx711_clock(hx, *time_us, false, changing_raw, false);
  }
  assert(hx->ready && hx->pulses == 24);
  *time_us += 2;
  hx711_clock(hx, *time_us, true, changing_raw, false);
  assert(!hx->ready && hx->dout && hx->pulses == 0);
  *time_us += 2;
  hx711_clock(hx, *time_us, false, changing_raw, false);
  return word;
}

static void test_hx711_serialization_and_latching(void) {
  hx711_model_t hx;
  uint64_t time_us = 0;
  hx711_init(&hx, time_us);
  hx711_update(&hx, 99999, 120000, false);
  assert(hx.dout && !hx.ready);
  time_us = 100000;
  hx711_update(&hx, time_us, 123456, false);
  assert(hx.ready && !hx.dout);
  assert(read_word(&hx, &time_us, 999999) == 123456); /* Read is one latched sample. */
  hx711_update(&hx, hx.next_ready_us - 1, -1234, false);
  assert(!hx.ready);
  time_us = hx.next_ready_us;
  hx711_update(&hx, time_us, -1234, false);
  uint32_t negative = read_word(&hx, &time_us, 0);
  assert((int32_t)(negative << 8) / 256 == -1234);
  time_us = hx.next_ready_us;
  hx711_update(&hx, time_us, 10000000, false);
  assert(read_word(&hx, &time_us, 0) == 8388607); /* HX711 signed 24-bit saturation. */
}

static void test_hx711_power_and_disconnect(void) {
  hx711_model_t hx;
  hx711_init(&hx, 0);
  hx711_update(&hx, 100000, 120000, false);
  hx711_clock(&hx, 100001, true, 120000, false);
  hx711_update(&hx, 100061, 120000, false);
  assert(hx.powered_down && !hx.ready && hx.dout);
  hx711_clock(&hx, 100062, false, 120000, false);
  assert(!hx.powered_down && hx.next_ready_us == 200062);
  hx711_update(&hx, 200062, 120000, false);
  assert(hx.ready);
  hx711_update(&hx, 200063, 120000, true);
  assert(!hx.ready && hx.dout);
  hx711_update(&hx, 999999, 120000, true);
  assert(!hx.ready && hx.dout);
  hx711_update(&hx, 1000000, 120000, false);
  assert(!hx.ready && hx.next_ready_us == 1100000);
  hx711_update(&hx, 1100000, 120000, false);
  assert(hx.ready && !hx.dout);
}

int main(void) {
  test_flow_and_feedback();
  test_faults_refill_and_overload();
  test_hx711_serialization_and_latching();
  test_hx711_power_and_disconnect();
  puts("PASS: conserved flow, ADC feedback, empty/block/refill/drain, gross overload, HX711 24+1 clocks, signed samples, power-down and disconnect");
  return 0;
}
