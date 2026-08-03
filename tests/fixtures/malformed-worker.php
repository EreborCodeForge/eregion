<?php
require_once __DIR__ . '/lib/worker_base.php';

$opts = eregion_parse_args($argv);

$path = $opts['socket'];
if (file_exists($path)) {
    @unlink($path);
}
$server = socket_create(AF_UNIX, SOCK_STREAM, 0);
socket_bind($server, $path);
socket_listen($server, 1);
$conn = socket_accept($server);

// Complete handshake normally.
$raw = eregion_read_frame($conn);
$hello = EregionMsgpack::unpack($raw);
$ready = [
    'type' => 'ready',
    'protocol' => 'eregion',
    'protocol_version' => 1,
    'worker_id' => $opts['worker_id'],
    'generation' => $opts['generation'],
    'pid' => getmypid(),
    'php_version' => PHP_VERSION,
    'mithril_version' => 'fixture',
];
eregion_write_frame($conn, EregionMsgpack::pack($ready));

// Read request then send garbage payload.
eregion_read_frame($conn);
socket_write($conn, pack('N', 4) . "XXXX");
socket_close($conn);
socket_close($server);
@unlink($path);
