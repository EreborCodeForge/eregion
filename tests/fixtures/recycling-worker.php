<?php
require_once __DIR__ . '/lib/worker_base.php';

$opts = eregion_parse_args($argv);
eregion_serve($opts, function (array $req, array $opts, int $handled) {
    $resp = eregion_ok_response($req, "recycle:$handled");
    if ($handled >= 2) {
        $resp['meta']['recycle'] = true;
        $resp['meta']['recycle_reason'] = 'planned';
    }
    return $resp;
});
