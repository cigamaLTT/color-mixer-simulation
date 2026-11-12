#include "mixer_model.h"

void mixer_init(mixer_model_t *model) {
  model->flow_ul_s = MIXER_DEFAULT_FLOW_UL_S;
  model->cup_mg = MIXER_DEFAULT_CUP_MG;
  model->pumps = model->stirrers = model->blocked = model->forced_empty = 0;
  for (unsigned i = 0; i < MIXER_CHANNELS; ++i) {
    model->source_ul[i] = MIXER_TANK_CAPACITY_UL;
    model->received_ul[i] = 0;
    model->flow_remainder[i] = 0;
  }
}

bool mixer_has_flow(const mixer_model_t *model, unsigned channel) {
  if (channel >= MIXER_CHANNELS) return false;
  const unsigned bit = 1u << channel;
  return (model->pumps & bit) &&
         !(model->blocked & bit) && !(model->forced_empty & bit) &&
         model->source_ul[channel] > 0;
}

void mixer_step(mixer_model_t *model, uint32_t elapsed_us) {
  for (unsigned i = 0; i < MIXER_CHANNELS; ++i) {
    if (!mixer_has_flow(model, i)) {
      continue;
    }
    const uint64_t numerator = (uint64_t)model->flow_ul_s * elapsed_us +
                               model->flow_remainder[i];
    uint32_t transferred = (uint32_t)(numerator / 1000000u);
    model->flow_remainder[i] = (uint32_t)(numerator % 1000000u);
    if (transferred > model->source_ul[i]) {
      transferred = model->source_ul[i];
      model->flow_remainder[i] = 0;
    }
    model->source_ul[i] -= transferred;
    model->received_ul[i] += transferred;
  }
}

void mixer_force_empty(mixer_model_t *model, uint8_t mask) {
  model->forced_empty = mask & 7u;
  for (unsigned i = 0; i < MIXER_CHANNELS; ++i) {
    if (model->forced_empty & (1u << i)) {
      model->source_ul[i] = 0;
      model->flow_remainder[i] = 0;
    }
  }
}

void mixer_refill(mixer_model_t *model) {
  for (unsigned i = 0; i < MIXER_CHANNELS; ++i) {
    if (!(model->forced_empty & (1u << i))) {
      model->source_ul[i] = MIXER_TANK_CAPACITY_UL;
      model->flow_remainder[i] = 0;
    }
  }
}

void mixer_drain_receiver(mixer_model_t *model) {
  for (unsigned i = 0; i < MIXER_CHANNELS; ++i) {
    model->received_ul[i] = 0;
  }
}

uint32_t mixer_net_mg(const mixer_model_t *model) {
  return model->received_ul[0] + model->received_ul[1] + model->received_ul[2];
}

uint32_t mixer_gross_mg(const mixer_model_t *model) {
  return model->cup_mg + mixer_net_mg(model);
}

float mixer_level_voltage(const mixer_model_t *model, unsigned channel) {
  return channel < MIXER_CHANNELS ?
      3.3f * (float)model->source_ul[channel] / MIXER_TANK_CAPACITY_UL : 0.0f;
}
