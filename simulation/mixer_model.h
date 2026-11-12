#ifndef MIXER_MODEL_H
#define MIXER_MODEL_H

#include <stdbool.h>
#include <stdint.h>

#define MIXER_CHANNELS 3
#define MIXER_TANK_CAPACITY_UL 1000000u
#define MIXER_SCALE_CAPACITY_MG 1000000u
#define MIXER_DEFAULT_CUP_MG 120000u
#define MIXER_DEFAULT_FLOW_UL_S 20000u

/* One microliter of modeled liquid weighs one milligram (density 1 g/mL). */
typedef struct {
  uint32_t source_ul[MIXER_CHANNELS];
  uint32_t received_ul[MIXER_CHANNELS];
  uint32_t flow_remainder[MIXER_CHANNELS];
  uint32_t flow_ul_s;
  uint32_t cup_mg;
  uint8_t pumps;
  uint8_t stirrers;
  uint8_t blocked;
  uint8_t forced_empty;
} mixer_model_t;

void mixer_init(mixer_model_t *model);
void mixer_step(mixer_model_t *model, uint32_t elapsed_us);
void mixer_refill(mixer_model_t *model);
void mixer_drain_receiver(mixer_model_t *model);
void mixer_force_empty(mixer_model_t *model, uint8_t mask);
uint32_t mixer_net_mg(const mixer_model_t *model);
uint32_t mixer_gross_mg(const mixer_model_t *model);
float mixer_level_voltage(const mixer_model_t *model, unsigned channel);
bool mixer_has_flow(const mixer_model_t *model, unsigned channel);

#endif
