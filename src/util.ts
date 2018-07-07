/**
 * This file contains (simplified) implementations of Lodash functions. We use these instead of depending on Lodash
 * because depending on it (1) either results in a significantly larger bundle size, if tree-shaking is not enabled
 * (and the npm lodash package is used), or (2) significantly increases the complexity of bundling and executing
 * code, if tree-shaking is enabled (and the npm lodash-es package is used).
 */

// tslint:disable-next-line:no-unbound-method
const toString = Object.prototype.toString

/** Reports whether value is a function. */
// tslint:disable-next-line:ban-types
export function isFunction(value: any): value is Function {
    return toString.call(value) === '[object Function]'
}

/** Flattens the array one level deep. */
export function flatten<T>(array: (T | T[])[]): T[] {
    const result: T[] = []
    for (const value of array) {
        if (Array.isArray(value)) {
            result.push(...value)
        } else {
            result.push(value)
        }
    }
    return result
}

/** Removes all falsey values. */
export function compact<T>(array: (T | null | undefined | false | '' | 0)[]): T[] {
    const result: T[] = []
    for (const value of array) {
        if (value) {
            result.push(value)
        }
    }
    return result
}

/** Reports whether the two values are equal, using a deep comparison. */
export function isEqual<T>(a: T, b: T): boolean {
    if (a === b) {
        return true
    }
    // tslint:disable-next-line:triple-equals
    if (a == null || b == null || (!isObjectLike(a) && !isObjectLike(b))) {
        return a !== a && b !== b
    }
    return equalObjects(a, b)
}

function equalObjects<T extends { [key: string]: any }>(a: T, b: T): boolean {
    if (isUndefinedOrNull(a) || isUndefinedOrNull(b)) {
        return false
    }

    const ka = Object.keys(a)
    const kb = Object.keys(b)
    if (ka.length !== kb.length) {
        return false
    }
    ka.sort()
    kb.sort()
    for (let i = ka.length - 1; i >= 0; i--) {
        if (ka[i] !== kb[i]) {
            return false
        }
    }
    for (let i = ka.length - 1; i >= 0; i--) {
        const key = ka[i]
        if (!isEqual(a[key], b[key])) {
            return false
        }
    }
    return typeof a === typeof b
}

function isObjectLike(value: any): value is object {
    return typeof value === 'object' && value !== null
}

function isUndefinedOrNull(value: any): value is undefined | null {
    return value === null || value === undefined
}
