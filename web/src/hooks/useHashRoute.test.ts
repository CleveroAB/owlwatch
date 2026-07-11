import { describe, expect, it } from 'vitest';
import { parseRoute } from './useHashRoute';

describe('parseRoute', () => {
  it('routes an empty or root hash to the overview', () => {
    expect(parseRoute('')).toEqual({ page: 'overview' });
    expect(parseRoute('#/')).toEqual({ page: 'overview' });
    expect(parseRoute('#/nonsense')).toEqual({ page: 'overview' });
  });

  it('routes #/s/{id} to that server, id verbatim', () => {
    expect(parseRoute('#/s/local')).toEqual({ page: 'server', id: 'local' });
    expect(parseRoute('#/s/db-1')).toEqual({ page: 'server', id: 'db-1' });
  });

  it('stops the id at /, ? and #', () => {
    expect(parseRoute('#/s/web1/extra')).toEqual({ page: 'server', id: 'web1' });
    expect(parseRoute('#/s/web1?x=1')).toEqual({ page: 'server', id: 'web1' });
    expect(parseRoute('#/s/web1#frag')).toEqual({ page: 'server', id: 'web1' });
  });

  it('never throws on malformed percent-encoding (no decodeURIComponent)', () => {
    // '#/s/100%' used to throw URIError inside the useState initializer and
    // white-screen the app — standalone instances included.
    expect(() => parseRoute('#/s/100%')).not.toThrow();
    expect(() => parseRoute('#/s/%E0%A4%A')).not.toThrow();
    // The bogus id passes through verbatim; App routes unknown ids to the
    // overview.
    expect(parseRoute('#/s/100%')).toEqual({ page: 'server', id: '100%' });
  });

  it('does not route an empty id segment', () => {
    expect(parseRoute('#/s/')).toEqual({ page: 'overview' });
  });
});
