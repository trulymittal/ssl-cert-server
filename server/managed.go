package server

import (
	"crypto/tls"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/jxskiss/gopkg/v2/zlog"
	"go.uber.org/zap"
)

const reloadInterval = 300 // seconds

type managedCert struct {
	sync.Mutex
	cert   unsafe.Pointer // *tls.Certificate
	loadAt int64
}

func NewManagedCertManager(stor *StorageManager, ocspMgr *OCSPManager) *ManagedCertManager {
	manager := &ManagedCertManager{
		stor:    stor,
		ocspMgr: ocspMgr,
		log:     zlog.Named("managed").Sugar(),
	}
	return manager
}

type ManagedCertManager struct {
	cache   sync.Map
	stor    *StorageManager
	ocspMgr *OCSPManager
	log     *zap.SugaredLogger
}

func (p *ManagedCertManager) Get(certKey string) (*tls.Certificate, error) {
	tlscert, err := p.getManagedCertificate(certKey)
	if err != nil {
		return nil, err
	}

	ocspKeyName := p.OCSPKeyName(certKey)
	p.ocspMgr.Watch(ocspKeyName, func() (*tls.Certificate, error) {
		return p.getManagedCertificate(certKey)
	})

	return tlscert, nil
}

func (p *ManagedCertManager) getManagedCertificate(certKey string) (*tls.Certificate, error) {
	cached, ok := p.cache.Load(certKey)
	if ok {
		mngCert := cached.(*managedCert)
		tlscert := atomic.LoadPointer(&mngCert.cert)
		if tlscert != nil {
			if mngCert.loadAt > 0 &&
				time.Now().Unix()-mngCert.loadAt > reloadInterval {
				go p.reloadManagedCertificate(mngCert, certKey)
			}
			return (*tls.Certificate)(tlscert), nil
		}
	}

	// certificate not cached, lock and load from storage
	cached, _ = p.cache.LoadOrStore(certKey, &managedCert{})
	mngCert := cached.(*managedCert)
	mngCert.Lock()
	defer mngCert.Unlock()

	if mngCert.cert != nil {
		return (*tls.Certificate)(mngCert.cert), nil
	}
	tlscert, _, _, err := p.stor.LoadCertificateFromStore(certKey)
	if err != nil {
		return nil, fmt.Errorf("managed: %v", err)
	}
	atomic.StorePointer(&mngCert.cert, unsafe.Pointer(tlscert))
	mngCert.loadAt = time.Now().Unix()
	return tlscert, nil
}

func (p *ManagedCertManager) reloadManagedCertificate(mngCert *managedCert, certKey string) {
	tlscert, _, _, err := p.stor.LoadCertificateFromStore(certKey)
	if err != nil {
		p.log.Warnf("failed reload certificate: certKey= %s err= %v", certKey, err)
		return
	}
	mngCert.Lock()
	defer mngCert.Unlock()
	atomic.StorePointer(&mngCert.cert, unsafe.Pointer(tlscert))
	mngCert.loadAt = time.Now().Unix()
}

func (p *ManagedCertManager) OCSPKeyName(certKey string) string {
	return fmt.Sprintf("managed|%s", certKey)
}
