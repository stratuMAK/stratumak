/* The cycle count behind the EtherCAT master's data-ok pin
 * (src/hal/drivers/ethercat/data_ok.h): low after loss_cycles incomplete
 * working counters in a row, high again on the first complete one. */
#include <stdio.h>

#include "data_ok.h"

static int fails;

/* Feed one cycle and compare the pin with what it has to be. */
static void cycle(int *bad, int complete, int loss, int want, const char *what) {
  int got = lcec_data_ok_update(bad, complete, loss);
  if (got != want) {
    printf("FAIL: %s: data-ok %d, expected %d (loss_cycles %d)\n", what, got, want, loss);
    fails++;
  }
}

static void run(int loss) {
  int bad = loss; /* as the driver starts it: low until the first complete cycle */
  int i;

  /* Bus coming up: incomplete cycles keep it low. */
  for (i = 0; i < 2 * loss + 2; i++) {
    cycle(&bad, 0, loss, 0, "start-up, incomplete");
  }
  /* The first complete cycle makes it high. */
  cycle(&bad, 1, loss, 1, "first complete cycle");

  /* loss - 1 incomplete cycles in a row do not take it down ... */
  for (i = 0; i < loss - 1; i++) {
    cycle(&bad, 0, loss, 1, "fewer than loss_cycles incomplete");
  }
  /* ... and a complete one resets the count. */
  cycle(&bad, 1, loss, 1, "complete after a gap");
  for (i = 0; i < loss - 1; i++) {
    cycle(&bad, 0, loss, 1, "count restarted after a complete cycle");
  }
  /* The loss_cycles-th incomplete cycle in a row does. */
  cycle(&bad, 0, loss, 0, "loss_cycles incomplete in a row");
  /* It stays low however long it lasts (the count saturates) ... */
  for (i = 0; i < 1000; i++) {
    cycle(&bad, 0, loss, 0, "still incomplete");
  }
  /* ... and the first complete cycle brings it back. */
  cycle(&bad, 1, loss, 1, "recovered");
}

int main(void) {
  run(1);
  run(LCEC_DATA_LOSS_CYCLES_DEFAULT);
  run(10);

  /* The default: one lost frame is not a loss. */
  if (LCEC_DATA_LOSS_CYCLES_DEFAULT != 3) {
    printf("FAIL: default dataLossCycles %d, expected 3\n", LCEC_DATA_LOSS_CYCLES_DEFAULT);
    fails++;
  }
  {
    int bad = LCEC_DATA_LOSS_CYCLES_DEFAULT;
    cycle(&bad, 1, LCEC_DATA_LOSS_CYCLES_DEFAULT, 1, "default: complete");
    cycle(&bad, 0, LCEC_DATA_LOSS_CYCLES_DEFAULT, 1, "default: one lost frame");
    cycle(&bad, 1, LCEC_DATA_LOSS_CYCLES_DEFAULT, 1, "default: complete again");
  }

  if (fails == 0) {
    printf("data-ok count: OK\n");
  }
  return fails != 0;
}
