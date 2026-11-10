<?php
require_once __DIR__ . '/lib/worker_base.php';

$opts = eregion_parse_args($argv);
eregion_serve($opts, function (array $req) {
    $resp = eregion_ok_response($req, 'plain-body');
    $resp['meta']['recycle'] = true;
    $resp['meta']['recycle_reason'] = 'App\\Security\\UserContext';
    return $resp;
});
