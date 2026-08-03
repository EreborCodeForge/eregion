<?php
require_once __DIR__ . '/lib/worker_base.php';

$opts = eregion_parse_args($argv);
eregion_serve($opts, function (array $req) {
    $resp = eregion_ok_response($req, 'mismatch');
    $resp['id'] = 'wrong-id';
    return $resp;
});
