#include "mixer_render.h"

#define RGBA(r, g, b) ((uint32_t)(r) | ((uint32_t)(g) << 8) | \
                       ((uint32_t)(b) << 16) | 0xff000000u)

static const uint32_t BACKGROUND = RGBA(10, 18, 31);
static const uint32_t CARD = RGBA(20, 33, 49);
static const uint32_t BORDER = RGBA(49, 68, 87);
static const uint32_t TEXT = RGBA(234, 242, 250);
static const uint32_t MUTED = RGBA(137, 158, 178);
static const uint32_t AQUA = RGBA(71, 223, 212);
static const uint32_t AMBER = RGBA(255, 192, 77);
static const uint32_t ALERT = RGBA(255, 103, 115);
static const uint32_t LIQUID[3] = {
  RGBA(66, 191, 121), RGBA(236, 176, 148), RGBA(69, 137, 237)
};
static uint32_t *canvas;

/* Small original 5x7 pixel alphabet; no font files or external assets. */
static const uint8_t LETTERS[26][7] = {
  {14,17,17,31,17,17,17}, {30,17,17,30,17,17,30},
  {14,17,16,16,16,17,14}, {30,17,17,17,17,17,30},
  {31,16,16,30,16,16,31}, {31,16,16,30,16,16,16},
  {14,17,16,23,17,17,15}, {17,17,17,31,17,17,17},
  {14,4,4,4,4,4,14}, {7,2,2,2,18,18,12},
  {17,18,20,24,20,18,17}, {16,16,16,16,16,16,31},
  {17,27,21,21,17,17,17}, {17,25,21,19,17,17,17},
  {14,17,17,17,17,17,14}, {30,17,17,30,16,16,16},
  {14,17,17,17,21,18,13}, {30,17,17,30,20,18,17},
  {15,16,16,14,1,1,30}, {31,4,4,4,4,4,4},
  {17,17,17,17,17,17,14}, {17,17,17,17,17,10,4},
  {17,17,17,21,21,21,10}, {17,17,10,4,10,17,17},
  {17,17,10,4,4,4,4}, {31,1,2,4,8,16,31}
};
static const uint8_t DIGITS[10][7] = {
  {14,17,19,21,25,17,14}, {4,12,4,4,4,4,14},
  {14,17,1,2,4,8,31}, {30,1,1,14,1,1,30},
  {2,6,10,18,31,2,2}, {31,16,16,30,1,1,30},
  {14,16,16,30,17,17,14}, {31,1,2,4,8,8,8},
  {14,17,17,14,17,17,14}, {14,17,17,15,1,1,14}
};

static void pixel(int x, int y, uint32_t color) {
  if (x >= 0 && y >= 0 && x < MIXER_DISPLAY_WIDTH && y < MIXER_DISPLAY_HEIGHT)
    canvas[y * MIXER_DISPLAY_WIDTH + x] = color;
}

static void rect(int x, int y, int w, int h, uint32_t color) {
  for (int row = y; row < y + h; ++row)
    for (int col = x; col < x + w; ++col) pixel(col, row, color);
}

static void outline(int x, int y, int w, int h, int thickness, uint32_t color) {
  rect(x, y, w, thickness, color);
  rect(x, y + h - thickness, w, thickness, color);
  rect(x, y, thickness, h, color);
  rect(x + w - thickness, y, thickness, h, color);
}

static void line(int x0, int y0, int x1, int y1, int width, uint32_t color) {
  const int dx = x1 > x0 ? x1 - x0 : x0 - x1;
  const int dy = y1 > y0 ? y1 - y0 : y0 - y1;
  const int sx = x0 < x1 ? 1 : -1;
  const int sy = y0 < y1 ? 1 : -1;
  int error = dx - dy;
  for (;;) {
    rect(x0 - width / 2, y0 - width / 2, width, width, color);
    if (x0 == x1 && y0 == y1) break;
    const int twice = 2 * error;
    if (twice > -dy) { error -= dy; x0 += sx; }
    if (twice < dx) { error += dx; y0 += sy; }
  }
}

static void disc(int x, int y, int radius, uint32_t color) {
  for (int row = -radius; row <= radius; ++row)
    for (int col = -radius; col <= radius; ++col)
      if (row * row + col * col <= radius * radius) pixel(x + col, y + row, color);
}

static uint8_t glyph_row(char ch, unsigned row) {
  if (ch >= 'a' && ch <= 'z') ch -= 'a' - 'A';
  if (ch >= 'A' && ch <= 'Z') return LETTERS[ch - 'A'][row];
  if (ch >= '0' && ch <= '9') return DIGITS[ch - '0'][row];
  if (ch == '.') return row == 6 ? 4 : 0;
  if (ch == ':') return row == 2 || row == 5 ? 4 : 0;
  if (ch == '-') return row == 3 ? 14 : 0;
  if (ch == '+') return row == 3 ? 14 : row >= 2 && row <= 4 ? 4 : 0;
  if (ch == '/') return row < 5 ? 1u << row : 0;
  if (ch == '%') {
    const uint8_t percent[7] = {25,26,2,4,8,11,19};
    return percent[row];
  }
  if (ch == '>') return row < 4 ? 1u << (3u - row) : 1u << (row - 3u);
  return 0;
}

static void text(int x, int y, const char *str, int scale, uint32_t color) {
  for (; *str; ++str, x += scale * 6) {
    for (unsigned row = 0; row < 7; ++row) {
      const uint8_t bits = glyph_row(*str, row);
      for (unsigned col = 0; col < 5; ++col)
        if (bits & (16u >> col))
          rect(x + (int)col * scale, y + (int)row * scale, scale, scale, color);
    }
  }
}

static char *append_uint(char *out, uint32_t value) {
  char reverse[12];
  unsigned count = 0;
  do { reverse[count++] = '0' + value % 10u; value /= 10u; } while (value);
  while (count) *out++ = reverse[--count];
  *out = 0;
  return out;
}

static void value(int x, int y, uint32_t thousandths, const char *unit,
                  int scale, uint32_t color, bool decimal) {
  char buf[32];
  char *end = append_uint(buf, thousandths / 1000u);
  if (decimal) { *end++ = '.'; *end++ = '0' + thousandths / 100u % 10u; }
  *end++ = ' ';
  while (*unit) *end++ = *unit++;
  *end = 0;
  text(x, y, buf, scale, color);
}

static void stirrer(int x, int top, int bottom, bool running, uint32_t phase) {
  disc(x, top, 12, running ? AQUA : BORDER);
  text(x - 3, top - 3, "M", 1, BACKGROUND);
  line(x, top + 12, x, bottom, 3, MUTED);
  int dx = 19, dy = 0;
  if (running) {
    const int directions[4][2] = {{19,0},{13,6},{0,8},{-13,6}};
    dx = directions[phase % 4][0]; dy = directions[phase % 4][1];
  }
  line(x - dx, bottom - dy, x + dx, bottom + dy, 4, running ? TEXT : MUTED);
}

static uint32_t mixed_color(const mixer_model_t *model) {
  uint32_t total = mixer_net_mg(model);
  if (!total) return RGBA(37, 63, 79);
  uint32_t channels[3] = {0,0,0};
  for (unsigned component = 0; component < 3; ++component)
    for (unsigned color = 0; color < 3; ++color)
      channels[color] += (uint32_t)((uint64_t)((LIQUID[component] >> (8 * color)) & 255u) *
                                   model->received_ul[component] / total);
  return RGBA(channels[0], channels[1], channels[2]);
}

void mixer_render(uint32_t *pixels, const mixer_model_t *model,
                  const mixer_render_state_t *state) {
  canvas = pixels;
  rect(0, 0, MIXER_DISPLAY_WIDTH, MIXER_DISPLAY_HEIGHT, BACKGROUND);
  text(20, 18, "COLOR MIXER", 3, TEXT);
  text(497, 17, "ESP32 / GPIO", 2, AQUA);
  text(498, 41, "CLOSED LOOP PLANT", 1, MUTED);
  line(20, 58, 740, 58, 1, BORDER);

  const char *names[] = {"01 GREEN", "02 SKIN", "03 BLUE"};
  for (unsigned i = 0; i < 3; ++i) {
    const int x = 18 + 249 * (int)i;
    const int center = x + 80;
    const bool pumping = model->pumps & (1u << i);
    const bool flowing = mixer_has_flow(model, i);
    const bool blocked = model->blocked & (1u << i);
    rect(x, 72, 226, 232, CARD);
    rect(x, 72, 226, 3, LIQUID[i]);
    text(x + 14, 88, names[i], 2, TEXT);
    text(x + 159, 119, "FLOAT", 1, MUTED);

    const int level = (int)((uint64_t)model->source_ul[i] * 126 / MIXER_TANK_CAPACITY_UL);
    rect(x + 14, 126, 132, 134, RGBA(11, 25, 38));
    rect(x + 18, 256 - level, 124, level, LIQUID[i]);
    outline(x + 14, 126, 132, 134, 3, BORDER);
    if (level) line(x + 19, 256 - level, x + 140, 256 - level, 2, TEXT);
    for (int mark = 0; mark <= 4; ++mark)
      line(x + 18, 132 + mark * 29, x + 25, 132 + mark * 29, 1, MUTED);
    stirrer(center, 122, 235, model->stirrers & (1u << i), state->frame_number);

    outline(x + 169, 136, 15, 109, 2, BORDER);
    const int float_y = 241 - (int)((uint64_t)model->source_ul[i] * 101 / MIXER_TANK_CAPACITY_UL);
    line(x + 163, float_y, x + 190, float_y, 5, AMBER);
    char percentage[8];
    char *end = append_uint(percentage, model->source_ul[i] / 10000u);
    *end++ = '%'; *end = 0;
    text(x + 156, 255, percentage, 2, model->source_ul[i] ? TEXT : ALERT);
    value(x + 14, 280, model->source_ul[i], "ML", 2, LIQUID[i], false);

    const uint32_t pipe = flowing ? LIQUID[i] : BORDER;
    line(center, 261, center, 314, 5, pipe);
    const int manifold_y = 356 + (int)i * 8;
    line(center, 349, center, manifold_y, 5, pipe);
    line(center, manifold_y, 380, manifold_y, 5, pipe);
    line(380, manifold_y, 380, 393, 5, pipe);
    disc(center, 332, 19, pumping && !flowing ? AMBER : pipe);
    disc(center, 332, 14, CARD);
    text(center - 5, 329, "P", 1, pumping ? TEXT : MUTED);
    if (flowing) {
      const int offset = (int)(state->frame_number % 3) * 8;
      line(center, 306 + offset, center - 3, 301 + offset, 2, TEXT);
      line(center, 306 + offset, center + 3, 301 + offset, 2, TEXT);
    }
    text(x + 111, 326, !model->source_ul[i] ? "EMPTY" : blocked ? "BLOCK" :
         pumping ? "RUN" : "OFF", 1, pumping && !flowing ? AMBER : MUTED);
  }

  /* Receiver on a load cell: the scale reports cup plus deposited liquid. */
  const uint32_t net = mixer_net_mg(model);
  const uint32_t gross = mixer_gross_mg(model);
  const uint32_t shown_net = net > 1000000u ? 1000000u : net;
  const int cup_fill = (int)((uint64_t)shown_net * 74 / 1000000u);
  rect(311, 393, 138, 85, CARD);
  rect(315, 474 - cup_fill, 130, cup_fill, mixed_color(model));
  outline(311, 393, 138, 85, 3, BORDER);
  stirrer(380, 395, 455, model->stirrers & 8u, state->frame_number);
  rect(298, 480, 164, 12, MUTED);
  text(323, 502, "RECEIVER / HX711", 1, MUTED);

  text(20, 394, "LIQUID", 1, MUTED);
  value(20, 412, net, "G", 3, TEXT, true);
  text(20, 445, "CUP", 1, MUTED);
  value(62, 445, model->cup_mg, "G", 1, MUTED, false);
  text(20, 465, "FLOW", 1, MUTED);
  value(62, 465, model->flow_ul_s, "ML/S", 1, MUTED, false);
  text(20, 493, "3 SOURCES / 4 STIRRERS", 1, MUTED);

  text(502, 394, "GROSS / 1KG MAX", 1, MUTED);
  if (state->scale_disconnected) {
    text(502, 412, "NO SCALE", 2, ALERT);
  } else if (state->measured_mg < 0) {
    text(502, 412, "-", 3, TEXT);
    value(522, 412, (uint32_t)-state->measured_mg, "G", 2, TEXT, true);
  } else {
    value(502, 412, (uint32_t)state->measured_mg, "G", 2,
          gross > MIXER_SCALE_CAPACITY_MG ? ALERT : AQUA, true);
  }
  outline(502, 439, 218, 9, 1, BORDER);
  const uint32_t scale_fraction = gross > 1000000u ? 1000000u : gross;
  rect(504, 441, (int)((uint64_t)scale_fraction * 214 / 1000000u), 5,
       gross > MIXER_SCALE_CAPACITY_MG ? ALERT : AQUA);
  text(502, 461, gross > MIXER_SCALE_CAPACITY_MG ? "OVERLOAD" :
       state->noise_mg ? "NOISY SCALE" : "SCALE READY", 1,
       gross > MIXER_SCALE_CAPACITY_MG ? ALERT : MUTED);
  text(502, 481, "1000 COUNTS/G / 10 HZ", 1, MUTED);
  text(502, 501, "SELECT PLANT FOR CONTROLS", 1, MUTED);
}
