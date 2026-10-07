import { describe, expect, it } from 'vitest'
import { ApiError, fieldErrors } from './client'

describe('fieldErrors', () => {
  it('groups server field errors by field', () => {
    const e = new ApiError(422, 'invalid_form', 'bad', [
      { field: 'envs', message: 'duplicate environment "dev"' },
      { field: 'envs', message: '"X": use lowercase letters' },
      { field: 'region', message: 'enter an AWS region' },
    ])
    expect(fieldErrors(e)).toEqual({
      envs: ['duplicate environment "dev"', '"X": use lowercase letters'],
      region: ['enter an AWS region'],
    })
  })
  it('ignores unrelated errors', () => {
    expect(fieldErrors(new Error('boom'))).toEqual({})
    expect(fieldErrors(new ApiError(500, 'x', 'y'))).toEqual({})
  })
})
