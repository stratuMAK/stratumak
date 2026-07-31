// The display-formatting rules carried over from the Tk linuxcnctop. These are
// 2.9 semantics, not cosmetics: a wrong axis-mask filter or a missed sequence
// number in active_gcodes shows the operator a machine that does not exist.
import { describe, it, expect } from 'vitest';
import { flattenStat, rowsToText } from '../src/stores/format';
import { makeStat } from './helpers';

function rowMap(stat = makeStat()) {
  return new Map(flattenStat(stat).map(r => [r.key, r.value]));
}

describe('positions', () => {
  it('shows only the axes in axis_mask', () => {
    const rows = rowMap(makeStat({ axis_mask: 0b111 }));
    expect(rows.get('position')).toBe('1.0000 2.0000 3.0000');
  });

  it('drops the axes the machine does not have', () => {
    // XZ lathe: bits 0 and 2.
    const rows = rowMap(makeStat({ axis_mask: 0b101 }));
    expect(rows.get('position')).toBe('1.0000 3.0000');
  });

  it('applies to every position-shaped field, including motion.dtg', () => {
    const rows = rowMap(makeStat({ axis_mask: 0b1 }));
    for (const key of ['position', 'actual_position', 'probed_position',
                       'g5x_offset', 'g92_offset', 'tool_offset', 'motion.dtg']) {
      expect(rows.get(key), key).toBe(rows.get(key)!.trim());
      expect(rows.get(key)!.split(' '), key).toHaveLength(1);
    }
  });
});

describe('active codes', () => {
  it('skips the sequence number at index 0 and the -1 inactive groups', () => {
    // active_gcodes[0] = 12 is the sequence number; it must NOT render as G1.2.
    const rows = rowMap();
    expect(rows.get('active_gcodes')).toBe('G1 G17 G90 G94');
  });

  it('renders M-codes without the /10 scaling G-codes need', () => {
    const rows = rowMap();
    expect(rows.get('active_mcodes')).toBe('M5 M9');
  });

  it('handles a fully inactive set', () => {
    const rows = rowMap(makeStat({ active_gcodes: [7, -1, -1], active_mcodes: [7, -1] }));
    expect(rows.get('active_gcodes')).toBe('-'); // empty renders as "-", as in Tk
    expect(rows.get('active_mcodes')).toBe('-');
  });
});

describe('per-joint arrays', () => {
  it('truncates the [MAX_JOINTS] wire arrays to the configured joint count', () => {
    const homed = new Array(16).fill(false);
    homed[0] = homed[1] = homed[2] = true;
    const rows = rowMap(makeStat({ joints_count: 3, homed }));
    expect(rows.get('homed')).toBe('true true true');
    expect(rows.get('joint_position')!.split(' ')).toHaveLength(3);
  });

  it('renders nothing for a machine with no joints', () => {
    const rows = rowMap(makeStat({ joints_count: 0 }));
    expect(rows.get('homed')).toBe('-');
  });
});

describe('enums', () => {
  it('renders names, not raw ints', () => {
    const rows = rowMap();
    expect(rows.get('task.mode')).toBe('MANUAL');
    expect(rows.get('task.state')).toBe('ON');
    expect(rows.get('task.interp_state')).toBe('IDLE');
    expect(rows.get('task.exec_state')).toBe('DONE');
    expect(rows.get('motion.mode')).toBe('FREE');
    expect(rows.get('kinematics_type')).toBe('IDENTITY');
  });

  it('falls back to the number for a value the IDL does not name', () => {
    const stat = makeStat();
    (stat.task as unknown as Record<string, number>).mode = 99;
    expect(rowMap(stat).get('task.mode')).toBe('99');
  });

  it('names the non-enum coded fields too', () => {
    const rows = rowMap(makeStat({ state: 2, rcs_status: 3 }));
    expect(rows.get('task.program_units')).toBe('mm');
    expect(rows.get('motion.motion_type')).toBe('none');
    expect(rows.get('state')).toBe('rcs_exec');
    expect(rows.get('rcs_status')).toBe('rcs_error');
  });
});

describe('nested records', () => {
  it('walks joints/spindle/axis into per-index rows', () => {
    const stat = makeStat({
      joints: [{ homed: true, velocity: 1.5 }, { homed: false, velocity: 0 }],
      spindle: [{ speed: 1000, direction: 1 }],
    } as unknown as Partial<ReturnType<typeof makeStat>>);
    const rows = rowMap(stat);
    expect(rows.get('joints.0.homed')).toBe('true');
    expect(rows.get('joints.1.homed')).toBe('false');
    expect(rows.get('spindle.0.speed')).toBe('1000');
  });

  it('renders an empty list as "-" rather than dropping the field', () => {
    expect(rowMap(makeStat({ joints: [] })).get('joints')).toBe('-');
  });

  // The server sends joints[] at its full MAX_JOINTS width and axis[] at
  // MAX_AXIS, as classic NML did — joints_count and axis_mask say which are
  // real. Rendering the rest buries the machine in rows of nonexistent hardware.
  it('cuts joints[] off at joints_count', () => {
    const joints = new Array(16).fill(null).map((_, i) => ({ homed: false, limit: i }));
    const rows = rowMap(makeStat({ joints, joints_count: 3 } as never));
    expect(rows.has('joints.2.limit')).toBe(true);
    expect(rows.has('joints.3.limit')).toBe(false);
    expect(rows.has('joints.15.limit')).toBe(false);
  });

  it('keeps only the axis[] entries in axis_mask, at their own index', () => {
    const axis = new Array(9).fill(null).map((_, i) => ({ velocity: i }));
    // XZ lathe: bits 0 and 2.
    const rows = rowMap(makeStat({ axis, axis_mask: 0b101 } as never));
    expect(rows.get('axis.0.velocity')).toBe('0');
    expect(rows.has('axis.1.velocity')).toBe(false);
    // Index preserved, so the row matches the axis letter the position uses.
    expect(rows.get('axis.2.velocity')).toBe('2');
    expect(rows.has('axis.3.velocity')).toBe(false);
  });

  it('leaves spindle[] alone — it arrives correctly sized', () => {
    const rows = rowMap(makeStat({ spindle: [{ speed: 1 }, { speed: 2 }] } as never));
    expect(rows.get('spindle.0.speed')).toBe('1');
    expect(rows.get('spindle.1.speed')).toBe('2');
  });

  it('renders "-" when the configuration selects nothing', () => {
    const joints = new Array(16).fill({ homed: false });
    const axis = new Array(9).fill({ velocity: 0 });
    const rows = rowMap(makeStat({ joints, joints_count: 0, axis, axis_mask: 0 } as never));
    expect(rows.get('joints')).toBe('-');
    expect(rows.get('axis')).toBe('-');
  });
});

describe('scalars', () => {
  it('renders the debug bitmask as the hex an INI [EMC]DEBUG carries', () => {
    expect(rowMap(makeStat({ debug: 0x7fffffff })).get('debug')).toBe('0x7fffffff');
  });

  it('renders bigint boot_id without throwing', () => {
    expect(rowMap(makeStat({ boot_id: 1234567890123456789n })).get('boot_id'))
      .toBe('1234567890123456789');
  });

  it('keeps integers integral and gives floats 4 decimals', () => {
    const rows = rowMap(makeStat({ heartbeat: 42, cycle_time: 0.001 }));
    expect(rows.get('heartbeat')).toBe('42');
    expect(rows.get('cycle_time')).toBe('0.0010');
  });
});

describe('rowsToText', () => {
  it('emits tab-separated key/value lines', () => {
    const text = rowsToText([{ key: 'a.b', value: '1' }, { key: 'c', value: '2' }]);
    expect(text).toBe('a.b\t1\nc\t2');
  });
});
