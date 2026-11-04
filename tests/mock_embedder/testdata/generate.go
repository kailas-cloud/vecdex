//go:build ignore

// Generate an isolated test-only CA/server certificate. This key is public fixture
// data and must never be used outside the local Bedrock mock network.
package main

import (
 "crypto/ecdsa"
 "crypto/elliptic"
 "crypto/rand"
 "crypto/x509"
 "crypto/x509/pkix"
 "encoding/pem"
 "math/big"
 "os"
 "time"
)

func main(){
 key,err:=ecdsa.GenerateKey(elliptic.P256(),rand.Reader);if err!=nil{panic(err)}
 certificate:=&x509.Certificate{SerialNumber:big.NewInt(1),Subject:pkix.Name{CommonName:"Vecdex test-only Bedrock mock"},
 DNSNames:[]string{"bedrock-runtime.us-east-1.amazonaws.com"},
 NotBefore:time.Date(2020,1,1,0,0,0,0,time.UTC),NotAfter:time.Date(2040,1,1,0,0,0,0,time.UTC),
 BasicConstraintsValid:true,IsCA:true,KeyUsage:x509.KeyUsageCertSign|x509.KeyUsageDigitalSignature,
 ExtKeyUsage:[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
 der,err:=x509.CreateCertificate(rand.Reader,certificate,certificate,&key.PublicKey,key);if err!=nil{panic(err)}
 encodedKey,err:=x509.MarshalECPrivateKey(key);if err!=nil{panic(err)}
 if err:=os.WriteFile("tests/mock_embedder/testdata/mock-bedrock.pem",pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE",Bytes:der}),0644);err!=nil{panic(err)}
 if err:=os.WriteFile("tests/mock_embedder/testdata/mock-bedrock-key.pem",pem.EncodeToMemory(&pem.Block{Type:"EC PRIVATE KEY",Bytes:encodedKey}),0600);err!=nil{panic(err)}
}
