#include "hx711_model.h"

void hx711_init(hx711_model_t *hx, uint64_t now_us) {
  hx->next_ready_us = now_us + HX711_CONVERSION_US;
  hx->high_since_us = now_us;
  hx->sample_bits = 0;
  hx->pulses = 0;
  hx->clock_high = hx->ready = hx->powered_down = hx->disconnected = false;
  hx->dout = true;
}

void hx711_update(hx711_model_t *hx, uint64_t now_us, int32_t raw_counts,
                  bool disconnected) {
  if (disconnected != hx->disconnected) {
    hx->disconnected = disconnected;
    hx->ready = false;
    hx->pulses = 0;
    hx->dout = true;
    hx->next_ready_us = now_us + HX711_CONVERSION_US;
  }
  if (disconnected) {
    hx->dout = true;
    return;
  }
  if (hx->clock_high && now_us - hx->high_since_us >= HX711_POWER_DOWN_US) {
    hx->powered_down = true;
    hx->ready = false;
    hx->pulses = 0;
    hx->dout = true;
  }
  if (!hx->powered_down && !hx->clock_high && !hx->ready &&
      now_us >= hx->next_ready_us) {
    if (raw_counts > 8388607) raw_counts = 8388607;
    if (raw_counts < -8388608) raw_counts = -8388608;
    hx->sample_bits = (uint32_t)raw_counts & 0xffffffu;
    hx->pulses = 0;
    hx->ready = true;
    hx->dout = false;
  }
}

void hx711_clock(hx711_model_t *hx, uint64_t now_us, bool high,
                 int32_t raw_counts, bool disconnected) {
  hx711_update(hx, now_us, raw_counts, disconnected);
  if (high == hx->clock_high) return;
  hx->clock_high = high;
  if (high) {
    hx->high_since_us = now_us;
    if (hx->ready && !disconnected) {
      if (hx->pulses < 24) {
        hx->dout = (hx->sample_bits >> (23u - hx->pulses)) & 1u;
        ++hx->pulses;
      } else {
        /* Pulse 25 selects channel A / gain 128 for the next conversion. */
        hx->dout = true;
        hx->ready = false;
        hx->pulses = 0;
        hx->next_ready_us = now_us + HX711_CONVERSION_US;
      }
    }
  } else if (hx->powered_down) {
    hx->powered_down = false;
    hx->ready = false;
    hx->pulses = 0;
    hx->dout = true;
    hx->next_ready_us = now_us + HX711_CONVERSION_US;
  }
}
