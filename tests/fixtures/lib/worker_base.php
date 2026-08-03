<?php
require_once __DIR__ . '/msgpack.php';

function eregion_parse_args(array $argv): array
{
    $out = [
        'socket' => null,
        'worker_id' => 'worker-1',
        'generation' => 1,
        'max_requests' => 0,
        'memory_limit_mb' => 0,
        'manifest' => null,
    ];
    foreach ($argv as $i => $arg) {
        if ($i === 0) {
            continue;
        }
        if (str_starts_with($arg, '--socket=')) {
            $out['socket'] = substr($arg, 9);
        } elseif (str_starts_with($arg, '--worker-id=')) {
            $out['worker_id'] = substr($arg, 12);
        } elseif (str_starts_with($arg, '--generation=')) {
            $out['generation'] = (int) substr($arg, 13);
        } elseif (str_starts_with($arg, '--max-requests=')) {
            $out['max_requests'] = (int) substr($arg, 15);
        } elseif (str_starts_with($arg, '--memory-limit-mb=')) {
            $out['memory_limit_mb'] = (int) substr($arg, 18);
        } elseif (str_starts_with($arg, '--manifest=')) {
            $out['manifest'] = substr($arg, 11);
        }
    }
    if ($out['socket'] === null || $out['socket'] === '') {
        fwrite(STDERR, "missing --socket\n");
        exit(2);
    }
    return $out;
}

function eregion_read_frame($sock): string
{
    $hdr = eregion_read_exact($sock, 4);
    $len = unpack('N', $hdr)[1];
    if ($len === 0 || $len > 16 * 1024 * 1024) {
        throw new RuntimeException('invalid frame length ' . $len);
    }
    return eregion_read_exact($sock, $len);
}

function eregion_write_frame($sock, string $payload): void
{
    $hdr = pack('N', strlen($payload));
    $data = $hdr . $payload;
    $written = 0;
    $n = strlen($data);
    while ($written < $n) {
        $w = socket_write($sock, substr($data, $written));
        if ($w === false) {
            throw new RuntimeException('socket_write failed');
        }
        $written += $w;
    }
}

function eregion_read_exact($sock, int $n): string
{
    $buf = '';
    while (strlen($buf) < $n) {
        $chunk = socket_read($sock, $n - strlen($buf), PHP_BINARY_READ);
        if ($chunk === false || $chunk === '') {
            throw new RuntimeException('socket closed');
        }
        $buf .= $chunk;
    }
    return $buf;
}

function eregion_serve(array $opts, callable $handler): void
{
    $path = $opts['socket'];
    if (file_exists($path)) {
        @unlink($path);
    }

    $server = socket_create(AF_UNIX, SOCK_STREAM, 0);
    if ($server === false) {
        fwrite(STDERR, "socket_create failed\n");
        exit(1);
    }
    if (!socket_bind($server, $path)) {
        fwrite(STDERR, "socket_bind failed\n");
        exit(1);
    }
    if (!socket_listen($server, 1)) {
        fwrite(STDERR, "socket_listen failed\n");
        exit(1);
    }
    @chmod($path, 0600);

    $conn = socket_accept($server);
    if ($conn === false) {
        fwrite(STDERR, "socket_accept failed\n");
        exit(1);
    }

    // Handshake
    $raw = eregion_read_frame($conn);
    $hello = EregionMsgpack::unpack($raw);
    if (($hello['type'] ?? '') !== 'hello') {
        fwrite(STDERR, "expected hello\n");
        exit(1);
    }
    $ready = [
        'type' => 'ready',
        'protocol' => 'eregion',
        'protocol_version' => 1,
        'worker_id' => $opts['worker_id'],
        'generation' => $opts['generation'],
        'pid' => getmypid(),
        'php_version' => PHP_VERSION,
        'mithril_version' => 'fixture-0.1.0',
    ];
    eregion_write_frame($conn, EregionMsgpack::pack($ready));

    $handled = 0;
    while (true) {
        try {
            $raw = eregion_read_frame($conn);
        } catch (Throwable $e) {
            break;
        }
        $req = EregionMsgpack::unpack($raw);
        $handled++;
        $resp = $handler($req, $opts, $handled);
        if (!isset($resp['meta'])) {
            $resp['meta'] = [];
        }
        $resp['meta']['requests_handled'] = $handled;
        if (!isset($resp['meta']['memory_usage'])) {
            $resp['meta']['memory_usage'] = memory_get_usage(true);
        }
        if (!isset($resp['meta']['memory_peak'])) {
            $resp['meta']['memory_peak'] = memory_get_peak_usage(true);
        }
        if (!isset($resp['meta']['recycle'])) {
            $resp['meta']['recycle'] = false;
        }
        if ($opts['max_requests'] > 0 && $handled >= $opts['max_requests']) {
            $resp['meta']['recycle'] = true;
            $resp['meta']['recycle_reason'] = 'max_requests';
        }
        eregion_write_frame($conn, EregionMsgpack::pack($resp));
        if (!empty($resp['meta']['recycle'])) {
            break;
        }
    }

    socket_close($conn);
    socket_close($server);
    @unlink($path);
    exit(0);
}

function eregion_ok_response(array $req, string $body, int $status = 200, array $headers = []): array
{
    if (!isset($headers['Content-Type'])) {
        $headers['Content-Type'] = ['text/plain'];
    }
    return [
        'type' => 'response',
        'version' => 1,
        'id' => $req['id'] ?? '',
        'status' => $status,
        'headers' => $headers,
        'body' => new EregionBin($body),
        'meta' => [
            'recycle' => false,
        ],
    ];
}
