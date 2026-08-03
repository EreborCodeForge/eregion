<?php
require_once __DIR__ . '/lib/worker_base.php';

$opts = eregion_parse_args($argv);
eregion_serve($opts, function (array $req) {
    $method = $req['method'] ?? 'GET';
    $path = $req['path'] ?? '/';
    $body = $req['body'] ?? '';
    if (is_array($body)) {
        $body = '';
    }

    if ($path === '/echo') {
        return eregion_ok_response($req, (string) $body, 200, [
            'Content-Type' => ['application/octet-stream'],
            'X-Echo-Method' => [$method],
        ]);
    }

    if ($path === '/headers') {
        $set = $req['headers']['X-Multi'] ?? [];
        return eregion_ok_response($req, json_encode(['multi' => $set]), 200, [
            'Content-Type' => ['application/json'],
            'Set-Cookie' => ['a=1', 'b=2'],
        ]);
    }

    if ($path === '/json') {
        return eregion_ok_response($req, (string) $body, 200, [
            'Content-Type' => ['application/json'],
        ]);
    }

    return eregion_ok_response($req, "ok:$method:$path", 200);
});
