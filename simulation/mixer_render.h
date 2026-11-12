#ifndef MIXER_RENDER_H
#define MIXER_RENDER_H

#include "mixer_model.h"

#define MIXER_DISPLAY_WIDTH 760
#define MIXER_DISPLAY_HEIGHT 520

typedef struct {
  uint32_t frame_number;
  uint32_t noise_mg;
  int32_t measured_mg;
  bool scale_disconnected;
} mixer_render_state_t;

/* Pixels are packed RGBA in memory, as required by the Wokwi framebuffer API. */
void mixer_render(uint32_t *pixels, const mixer_model_t *model,
                  const mixer_render_state_t *state);

#endif
