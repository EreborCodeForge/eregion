<?php
/**
 * Minimal MessagePack codec for Eregion test fixtures.
 * Supports maps, arrays, strings, binaries, ints, bools, nil.
 */
final class EregionMsgpack
{
    public static function pack($value): string
    {
        if ($value === null) {
            return "\xc0";
        }
        if (is_bool($value)) {
            return $value ? "\xc3" : "\xc2";
        }
        if (is_int($value)) {
            return self::packInt($value);
        }
        if (is_float($value)) {
            return "\xcb" . pack('E', $value);
        }
        if (is_string($value)) {
            // Binary-safe: treat as string (str*) unless marked.
            return self::packStr($value);
        }
        if ($value instanceof EregionBin) {
            return self::packBin($value->bytes);
        }
        if (is_array($value)) {
            if (array_is_list($value)) {
                return self::packArray($value);
            }
            return self::packMap($value);
        }
        throw new InvalidArgumentException('unsupported type');
    }

    public static function unpack(string $data)
    {
        $offset = 0;
        $value = self::unpackAt($data, $offset);
        return $value;
    }

    private static function packInt(int $n): string
    {
        if ($n >= 0) {
            if ($n <= 0x7f) {
                return chr($n);
            }
            if ($n <= 0xff) {
                return "\xcc" . chr($n);
            }
            if ($n <= 0xffff) {
                return "\xcd" . pack('n', $n);
            }
            if ($n <= 0xffffffff) {
                return "\xce" . pack('N', $n);
            }
            return "\xcf" . pack('J', $n);
        }
        if ($n >= -32) {
            return chr($n & 0xff);
        }
        if ($n >= -128) {
            return "\xd0" . pack('c', $n);
        }
        if ($n >= -32768) {
            return "\xd1" . pack('n', $n & 0xffff);
        }
        if ($n >= -2147483648) {
            return "\xd2" . pack('N', $n & 0xffffffff);
        }
        return "\xd3" . pack('J', $n);
    }

    private static function packStr(string $s): string
    {
        $len = strlen($s);
        if ($len <= 31) {
            return chr(0xa0 | $len) . $s;
        }
        if ($len <= 0xff) {
            return "\xd9" . chr($len) . $s;
        }
        if ($len <= 0xffff) {
            return "\xda" . pack('n', $len) . $s;
        }
        return "\xdb" . pack('N', $len) . $s;
    }

    private static function packBin(string $s): string
    {
        $len = strlen($s);
        if ($len <= 0xff) {
            return "\xc4" . chr($len) . $s;
        }
        if ($len <= 0xffff) {
            return "\xc5" . pack('n', $len) . $s;
        }
        return "\xc6" . pack('N', $len) . $s;
    }

    private static function packArray(array $arr): string
    {
        $n = count($arr);
        if ($n <= 15) {
            $out = chr(0x90 | $n);
        } elseif ($n <= 0xffff) {
            $out = "\xdc" . pack('n', $n);
        } else {
            $out = "\xdd" . pack('N', $n);
        }
        foreach ($arr as $v) {
            $out .= self::pack($v);
        }
        return $out;
    }

    private static function packMap(array $map): string
    {
        $n = count($map);
        if ($n <= 15) {
            $out = chr(0x80 | $n);
        } elseif ($n <= 0xffff) {
            $out = "\xde" . pack('n', $n);
        } else {
            $out = "\xdf" . pack('N', $n);
        }
        foreach ($map as $k => $v) {
            $out .= self::pack((string) $k);
            $out .= self::pack($v);
        }
        return $out;
    }

    private static function unpackAt(string $data, int &$offset)
    {
        if ($offset >= strlen($data)) {
            throw new RuntimeException('unexpected end');
        }
        $b = ord($data[$offset]);
        $offset++;

        if ($b <= 0x7f) {
            return $b;
        }
        if ($b >= 0xe0) {
            return $b - 256;
        }
        if (($b & 0xe0) === 0xa0) {
            $len = $b & 0x1f;
            $s = substr($data, $offset, $len);
            $offset += $len;
            return $s;
        }
        if (($b & 0xf0) === 0x90) {
            return self::unpackArray($data, $offset, $b & 0x0f);
        }
        if (($b & 0xf0) === 0x80) {
            return self::unpackMap($data, $offset, $b & 0x0f);
        }

        return match ($b) {
            0xc0 => null,
            0xc2 => false,
            0xc3 => true,
            0xc4 => self::readBin($data, $offset, self::readU8($data, $offset)),
            0xc5 => self::readBin($data, $offset, self::readU16($data, $offset)),
            0xc6 => self::readBin($data, $offset, self::readU32($data, $offset)),
            0xcc => self::readU8($data, $offset),
            0xcd => self::readU16($data, $offset),
            0xce => self::readU32($data, $offset),
            0xcf => self::readU64($data, $offset),
            0xd0 => self::readI8($data, $offset),
            0xd1 => self::readI16($data, $offset),
            0xd2 => self::readI32($data, $offset),
            0xd3 => self::readI64($data, $offset),
            0xd9 => self::readStr($data, $offset, self::readU8($data, $offset)),
            0xda => self::readStr($data, $offset, self::readU16($data, $offset)),
            0xdb => self::readStr($data, $offset, self::readU32($data, $offset)),
            0xdc => self::unpackArray($data, $offset, self::readU16($data, $offset)),
            0xdd => self::unpackArray($data, $offset, self::readU32($data, $offset)),
            0xde => self::unpackMap($data, $offset, self::readU16($data, $offset)),
            0xdf => self::unpackMap($data, $offset, self::readU32($data, $offset)),
            default => throw new RuntimeException(sprintf('unsupported msgpack byte 0x%02x', $b)),
        };
    }

    private static function unpackArray(string $data, int &$offset, int $n): array
    {
        $out = [];
        for ($i = 0; $i < $n; $i++) {
            $out[] = self::unpackAt($data, $offset);
        }
        return $out;
    }

    private static function unpackMap(string $data, int &$offset, int $n): array
    {
        $out = [];
        for ($i = 0; $i < $n; $i++) {
            $k = self::unpackAt($data, $offset);
            $v = self::unpackAt($data, $offset);
            $out[(string) $k] = $v;
        }
        return $out;
    }

    private static function readU8(string $data, int &$offset): int
    {
        $v = ord($data[$offset]);
        $offset++;
        return $v;
    }

    private static function readU16(string $data, int &$offset): int
    {
        $v = unpack('n', substr($data, $offset, 2))[1];
        $offset += 2;
        return $v;
    }

    private static function readU32(string $data, int &$offset): int
    {
        $v = unpack('N', substr($data, $offset, 4))[1];
        $offset += 4;
        return $v;
    }

    private static function readU64(string $data, int &$offset): int
    {
        $v = unpack('J', substr($data, $offset, 8))[1];
        $offset += 8;
        return $v;
    }

    private static function readI8(string $data, int &$offset): int
    {
        $v = unpack('c', $data[$offset])[1];
        $offset++;
        return $v;
    }

    private static function readI16(string $data, int &$offset): int
    {
        $u = self::readU16($data, $offset);
        return $u >= 0x8000 ? $u - 0x10000 : $u;
    }

    private static function readI32(string $data, int &$offset): int
    {
        $u = self::readU32($data, $offset);
        if ($u >= 0x80000000) {
            return $u - 0x100000000;
        }
        return $u;
    }

    private static function readI64(string $data, int &$offset): int
    {
        return self::readU64($data, $offset);
    }

    private static function readStr(string $data, int &$offset, int $len): string
    {
        $s = substr($data, $offset, $len);
        $offset += $len;
        return $s;
    }

    private static function readBin(string $data, int &$offset, int $len): string
    {
        return self::readStr($data, $offset, $len);
    }
}

final class EregionBin
{
    public function __construct(public string $bytes) {}
}
