#ifndef HX711_MODEL_H
#define HX711_MODEL_H

#include <stdbool.h>
#include <stdint.h>

#define HX711_CONVERSION_US 100000u
#define HX711_POWER_DOWN_US 60u

typedef struct {
  uint64_t next_ready_us;
  uint64_t high_since_us;
  uint32_t sample_bits;
  uint8_t pulses;
  bool clock_high;
  bool ready;
  bool powered_down;
  bool disconnected;
  bool dout;
} hx711_model_t;

void hx711_init(hx711_model_t *hx, uint64_t now_us);
void hx711_update(hx711_model_t *hx, uint64_t now_us, int32_t raw_counts,
                  bool disconnected);
void hx711_clock(hx711_model_t *hx, uint64_t now_us, bool high,
                 int32_t raw_counts, bool disconnected);

#endif
